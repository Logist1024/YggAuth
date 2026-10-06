// Package bootstrap 负责「首次启动时把管理员建出来」这一件事。
//
// 它是三层配置模型里的 L2:一次性、只在「库里还没有管理员」的时刻生效,
// 之后 env 里的相关项(ADMIN_*)既不读也不覆盖 —— 契约一句话:
// **env 是首次启动的种子,setting 是运行时的现值**(见 docs/configuration.md)。
//
// 三条硬约束:
//
//  1. 幂等:重复启动第二次必然 skip,不会重复建号、不会重设 installed_at;
//  2. 不复活:管理员被删光后**不会**自动补号。补号只可能来自
//     `yggauth admin create` 这种显式动作,静默补号会把「误删」
//     变成「删不掉」,也会在事故里悄悄引入一个新入口;
//  3. 单事务:建号、授权、审计、写 installed_at 全在一个事务里,
//     中途失败不会留下「有管理员但没有审计」这类半成品。
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yggauth/yggauth/internal/domain"
	"github.com/yggauth/yggauth/internal/identity/account"
	"github.com/yggauth/yggauth/internal/platform/db"
	"github.com/yggauth/yggauth/internal/platform/db/query"
	"github.com/yggauth/yggauth/internal/platform/rand"
)

// AdminRole 是默认授予的角色,同时也是「已初始化」的判据。
//
// 角色与它的权限点在 00001 迁移里种下,所以任何完成过基线迁移的库
// 都有这个角色;持它的账号数为 0 才算「还没引导过」。
const AdminRole = "platform_admin"

// InstalledAtKey 写进 app.setting,标记引导完成时刻。
const InstalledAtKey = "system.installed_at"

// Options 是引导入参。
type Options struct {
	// Enabled 对应 ADMIN_BOOTSTRAP。false 时首启不建号,只给出手工入口提示。
	Enabled bool
	// Email / Username 是默认管理员身份(ADMIN_EMAIL / ADMIN_USERNAME)。
	Email    string
	Username string
	// Password 为空表示随机生成(ADMIN_PASSWORD 留空的推荐姿势)。
	Password string
	// Policy 是密码策略:引导给的密码也必须过关,
	// 否则运维会在「能登进去但改不了密码」的处境里。
	Policy account.Policy
	// MustChange 为真时置「首登强制改密」旗标(凭据行真源,登录时投到会话行)。
	// Ensure 恒为 true;CLI 默认也是 true,只有运维显式说不要才关。
	MustChange bool
	// LoginEnabled 对应账号的通用布尔开关(新账号默认值)。
	LoginEnabled bool
}

// Kind 是引导结果种类,对应四种启动日志结尾。
type Kind string

const (
	// KindCreated 本次真的建了管理员。
	KindCreated Kind = "created"
	// KindSkipped 库里已经有管理员(或已有显式引导完成标记)。
	KindSkipped Kind = "skipped"
	// KindDisabled ADMIN_BOOTSTRAP=false,不做事。
	KindDisabled Kind = "disabled"
)

// Outcome 是引导结果。
type Outcome struct {
	Kind Kind
	// Username / Email 是本次结果涉及的身份(未创建时为空)。
	Username string
	Email    string
	// GeneratedPassword 只在「随机生成」时非空,**只在此处返回一次**,
	// 不写库、不进结构化日志的字段之外的地方。
	GeneratedPassword string
	// Reason 是给人看的一句话,用于启动日志。
	Reason string
	// NextStep 是运维下一步该做什么;非空时打 warn。
	NextStep string
}

// Ensure 执行首启引导,幂等。
//
// 正常三种结果(created / skipped / disabled)以 Outcome 返回,由调用方
// 按 Kind 打对应的启动日志结尾;真正的失败以 error 返回,调用方应当中止启动 ——
// 引导失败意味着配置或数据库坏了,带着半套状态继续跑更难排查。
func Ensure(ctx context.Context, pool *db.Pool, hasher *account.Hasher, opts Options) (Outcome, error) {
	if !opts.Enabled {
		return Outcome{
			Kind:     KindDisabled,
			Reason:   "ADMIN_BOOTSTRAP=false",
			NextStep: "需要管理员时:用 `yggauth admin create` 手工建号,或改回 ADMIN_BOOTSTRAP=true",
		}, nil
	}

	// 首启引导恒定要求首登改密:这个密码要么是运维填进 .env 的初始值,
	// 要么是随机生成、只在日志里打印一次的 —— 两者都不该继续被使用。
	opts.MustChange = true

	q := query.New(pool)
	done, err := initialized(ctx, q)
	if err != nil {
		return Outcome{}, fmt.Errorf("判断引导状态失败: %w", err)
	}
	if done {
		return Outcome{
			Kind:   KindSkipped,
			Reason: "库里已有平台管理员",
		}, nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("开启引导事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := create(ctx, query.New(pool).WithTx(tx), hasher, opts, true)
	if err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			// 事务已被唯一索引中止,不能再提交 —— defer 的 Rollback 收尾,
			// 对外只报 skip(它与「已经建过」是同一件事)。
			return out, nil
		}
		return Outcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{}, fmt.Errorf("提交引导事务失败: %w", err)
	}
	return out, nil
}

// ErrAlreadyExists 表示目标身份已被占用。
//
// 它是内部信号:撞上唯一索引后事务已经处于 aborted 状态,
// 调用方必须放弃提交并把它当成 skip —— 直接返回 (skipped, nil)
// 会在 Commit 时得到「commit unexpectedly resulted in rollback」,
// 把一个正常的「已存在」报成莫名其妙的失败。
var ErrAlreadyExists = errors.New("管理员身份已存在")

// Create 显式建一个平台管理员(CLI 用)。
//
// 与 Ensure 的区别只有一个:不检查「是否已初始化」——
// 手工补第二个管理员是运维的明确意图。同一身份重复创建会被唯一索引
// 挡成 skip,不会覆盖已有账号的密码。
func Create(ctx context.Context, pool *db.Pool, hasher *account.Hasher, opts Options) (Outcome, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("开启建号事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := create(ctx, query.New(pool).WithTx(tx), hasher, opts, false)
	if err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return out, nil
		}
		return Outcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{}, fmt.Errorf("提交建号事务失败: %w", err)
	}
	return out, nil
}

// initialized 判断该库是否**永远**不再需要自动引导。
//
// 两个判据,命中任意一个即算已引导:
//
//  1. system.installed_at 存在 —— 首启引导写下的永久标记。
//     它的意义正是「引导这件事已经发生过」,与当前库里还有没有管理员无关:
//     管理员随后被删光,重启得到的也是 skip 而不是悄悄补号
//     (包注释第 2 条:补号只能显式发起,否则「误删」会变成「删不掉」);
//  2. platform_admin 有持有者 —— 覆盖「关掉 ADMIN_BOOTSTRAP、
//     用 yggauth admin create 手工建的号」:那条路不写 installed_at,
//     但显然也不该在下次启动时再被塞一个默认管理员。
//
// 普通注册出来的用户**不**构成判据:一个部署可以先注册一堆用户、
// 还没建管理员,那时引导仍然该补上管理员。
func initialized(ctx context.Context, q *query.Queries) (bool, error) {
	if _, err := q.GetSetting(ctx, InstalledAtKey); err == nil {
		return true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	role, err := q.GetRoleByCode(ctx, AdminRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 迁移还没跑到种角色那步。交给引导建号(它会先失败并说明原因),
			// 而不是在这里假装「已初始化」。
			return false, nil
		}
		return false, err
	}
	n, err := q.CountAccountsWithRole(ctx, role.ID)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// create 是 Ensure/Create 共用的事务内实现。
//
// needRoleLock 为真时先锁角色行:并发启动(容器多副本同时跑迁移后的首启)
// 会在这里串行,第二个事务重查后发现已有管理员,直接返回 skip。
func create(ctx context.Context, q *query.Queries, hasher *account.Hasher, opts Options, needRoleLock bool) (Outcome, error) {
	username := strings.TrimSpace(opts.Username)
	email := strings.TrimSpace(opts.Email)
	if err := account.ValidateUsername(username); err != nil {
		return Outcome{}, fmt.Errorf("ADMIN_USERNAME 不合法: %w", err)
	}
	if err := account.ValidateEmail(email); err != nil {
		return Outcome{}, fmt.Errorf("ADMIN_EMAIL 不合法: %w", err)
	}

	if needRoleLock {
		// 用角色行当锁:有语义、不引入魔数,且迁移已保证这行存在。
		if _, err := q.GetRoleForUpdate(ctx, AdminRole); err != nil {
			return Outcome{}, fmt.Errorf("锁定管理员角色失败(迁移是否已执行?): %w", err)
		}
		// 拿到锁后再查一次:可能另一个副本刚建完。
		if done, err := initialized(ctx, q); err != nil {
			return Outcome{}, err
		} else if done {
			return Outcome{Kind: KindSkipped, Reason: "并发引导:另一个实例已完成建号"}, nil
		}
	}

	password := strings.TrimSpace(opts.Password)
	generated := false
	if password == "" {
		// 24 位 URL-safe ≈ 143 bit 熵,远超密码策略下限,
		// 且只打印一次 —— 比「先塞进 .env 再忘掉改」安全。
		p, err := rand.URLSafe(18)
		if err != nil {
			return Outcome{}, fmt.Errorf("生成随机管理员密码失败: %w", err)
		}
		password = p
		generated = true
	}
	if err := opts.Policy.Validate(password); err != nil {
		// 引导给的密码必须当场过关:「建好了但登不进去/改不动」
		// 是最难排查的一类事故,宁可启动失败。
		return Outcome{}, fmt.Errorf("管理员密码不满足策略,拒绝引导: %w", err)
	}

	hash, err := hasher.Hash(password)
	if err != nil {
		return Outcome{}, fmt.Errorf("计算密码哈希失败: %w", err)
	}

	role, err := q.GetRoleByCode(ctx, AdminRole)
	if err != nil {
		return Outcome{}, fmt.Errorf("查询管理员角色失败: %w", err)
	}

	acc, err := q.CreateAccount(ctx, query.CreateAccountParams{
		Username:       username,
		UsernameLower:  strings.ToLower(username),
		Email:          email,
		Status:         string(domain.StatusActive),
		McLoginEnabled: opts.LoginEnabled,
	})
	if err != nil {
		// 唯一索引是并发与重复执行的最后一道防线。
		// 注意事务已被中止:必须把哨兵错误传出去,调用方才不会去提交。
		if db.IsUniqueViolation(err) {
			// 两个都包:errors.Is 认哨兵,errors.Is(err, 底层) 也还成立 ——
			// 只包哨兵会让排查时看不到真正的数据库错误文本。
			return Outcome{Kind: KindSkipped, Reason: "该邮箱或用户名已存在"},
				fmt.Errorf("%w: %w", ErrAlreadyExists, err)
		}
		return Outcome{}, fmt.Errorf("创建管理员账号失败: %w", err)
	}

	// 管理员邮箱是运维自己填的,可能压根收不到信(mailbox=console 时也没人看),
	// 所以直接记为已验证 —— 否则默认的 pending 状态会让它登不进去。
	if _, err := q.SetAccountEmailVerified(ctx, acc.ID); err != nil {
		return Outcome{}, fmt.Errorf("标记管理员邮箱已验证失败: %w", err)
	}

	if _, err := q.UpsertCredential(ctx, query.UpsertCredentialParams{
		AccountID: acc.ID,
		Algo:      string(account.AlgoArgon2id),
		Hash:      hash,
		Params:    []byte("{}"),
	}); err != nil {
		return Outcome{}, fmt.Errorf("写入管理员凭据失败: %w", err)
	}

	// 首登强制改密(真源在凭据行,登录时投到会话行,见迁移 00008)
	if err := q.SetCredentialMustChange(ctx, query.SetCredentialMustChangeParams{
		AccountID:  acc.ID,
		Algo:       string(account.AlgoArgon2id),
		MustChange: opts.MustChange,
	}); err != nil {
		return Outcome{}, fmt.Errorf("设置强制改密标记失败: %w", err)
	}

	if _, err := q.GrantRole(ctx, query.GrantRoleParams{
		AccountID: acc.ID,
		RoleID:    role.ID,
		// 系统授予,没有「谁」:granted_by 留 NULL,而不是把账号自己
		// 写成自己的授予人。
		GrantedBy: pgtype.UUID{},
	}); err != nil {
		return Outcome{}, fmt.Errorf("授予平台管理员角色失败: %w", err)
	}

	// 两个入口共用这段代码,审计里必须能分辨是谁建的。
	// needRoleLock 只有「启动引导」为真(CLI 单次执行,不需要抢那把锁),
	// 它恰好就是入口的天然区分 —— 不必再往 Options 里塞一个只有实现知道的字段。
	via := "cli"
	if needRoleLock {
		via = "serve"
	}
	meta, err := json.Marshal(map[string]any{
		"role":               AdminRole,
		"password_generated": generated,
		// 绝不写入密码本身:审计表会被读、被导出、被展示在后台
		"must_change": opts.MustChange,
		"via":         via,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("编码审计元数据失败: %w", err)
	}
	if _, err := q.InsertAuditEvent(ctx, query.InsertAuditEventParams{
		AccountID:  pgUUID(acc.ID),
		Actor:      string(domain.AuditActorSystem()),
		Action:     "system.bootstrap",
		TargetType: pgText("account"),
		TargetID:   pgText(acc.ID.String()),
		Outcome:    string(domain.OutcomeSuccess),
		Metadata:   meta,
	}); err != nil {
		return Outcome{}, fmt.Errorf("写入引导审计失败: %w", err)
	}

	installedAt, err := json.Marshal(time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return Outcome{}, fmt.Errorf("编码 installed_at 失败: %w", err)
	}
	if _, err := q.UpsertSetting(ctx, query.UpsertSettingParams{
		Key:       InstalledAtKey,
		Value:     installedAt,
		UpdatedBy: pgUUID(acc.ID),
	}); err != nil {
		return Outcome{}, fmt.Errorf("写入 system.installed_at 失败: %w", err)
	}

	out := Outcome{
		Kind:     KindCreated,
		Username: username,
		Email:    email,
		Reason:   "首次启动,已创建平台管理员",
	}
	if generated {
		out.GeneratedPassword = password
		out.Reason = "首次启动,已创建平台管理员(密码随机生成,仅打印一次)"
	}
	return out, nil
}

// pgUUID / pgText 把普通类型换成 sqlc 需要的 pgtype 值。
//
// 可空列一律用零值(Valid=false)表达 NULL:引导是系统动作,
// 没有「操作者账号」,把目标账号自己写成操作者只会污染审计语义。
func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
