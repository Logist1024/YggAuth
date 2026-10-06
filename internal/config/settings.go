package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yggauth/yggauth/internal/platform/mailer"
)

// Settings 是运行时配置的**读取**接口(docs/configuration.md §6.1)。
//
// 注册、密码策略是热路径,不能每来一个请求查一次库 —— 实现全部走内存,
// 这里只暴露三个标量读法,调用方连「值是 JSON」都不用知道。
type Settings interface {
	String(ctx context.Context, key, fallback string) string
	Int(ctx context.Context, key string, fallback int) int
	Bool(ctx context.Context, key string, fallback bool) bool
}

// Sealer 是敏感配置值的加解密能力,由 keys.Manager 提供。
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Unseal(enc []byte) ([]byte, error)
}

// SettingsOptions 是 Snapshot 的构造参数。
type SettingsOptions struct {
	// Store 是底层读写。
	Store *SettingStore
	// Sealer 缺省时,写入 Secret 类键会直接报错(而不是明文落库)。
	Sealer Sealer
	// TTL 是内存快照的刷新周期。0 取 30s:兜底「有人手工改库」与
	// 「将来出现第二个进程」两种绕过后台的情况。
	TTL time.Duration
	// Cfg 是 env 种子,Sync 用它回填;nil 表示不回填。
	Cfg *Config
	// Logger 缺省时静默。
	Logger *slog.Logger
}

// cached 是一个键的内存副本。
type cached struct {
	value json.RawMessage
	// present 为假表示库里没有这一行(读取时用调用方的 fallback)。
	present bool
	// updatedAt 仅用于后台展示。
	updatedAt string
}

// Snapshot 是 Settings 的实现:全表载入内存 + 30s TTL + 写后原地更新。
//
// 用接口而不是直接把 *SettingStore 交给调用方:将来要换成
// LISTEN/NOTIFY 或 Redis,调用方一行不改。
type Snapshot struct {
	store  *SettingStore
	sealer Sealer
	ttl    time.Duration
	cfg    *Config
	log    *slog.Logger

	mu sync.RWMutex
	// values 是全表副本。写入后原地替换那一个键 → 单进程内立刻生效。
	values map[string]cached
	// loadedAt 是整表加载时间;超过 TTL 就重新加载一轮。
	loadedAt time.Time
	// version 由每次 Upsert 自增:整表加载期间若有人写过,
	// 这次加载的结果就作废重来 —— 否则「后台刚改完又被旧值盖回去」
	// 会表现成一个几乎无法复现的偶发失效。
	version uint64
}

// NewSnapshot 创建运行时配置快照。
func NewSnapshot(opts SettingsOptions) *Snapshot {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	lg := opts.Logger
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(nopWriter{}, nil))
	}
	return &Snapshot{
		store:  opts.Store,
		sealer: opts.Sealer,
		ttl:    ttl,
		cfg:    opts.Cfg,
		log:    lg,
		values: map[string]cached{},
	}
}

// ---------------------------------------------------------------- 读

// String 取字符串值。
func (s *Snapshot) String(ctx context.Context, key, fallback string) string {
	raw, ok := s.lookup(ctx, key)
	if !ok {
		return fallback
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// Int 取整型值。
func (s *Snapshot) Int(ctx context.Context, key string, fallback int) int {
	raw, ok := s.lookup(ctx, key)
	if !ok {
		return fallback
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// Bool 取布尔值。
func (s *Snapshot) Bool(ctx context.Context, key string, fallback bool) bool {
	raw, ok := s.lookup(ctx, key)
	if !ok {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

// MailerConfig 返回 mail.* 的**现值**,交给发件器热重建
// (docs/configuration.md §7.3)。
//
// 两处讲究:
//   - 密码解不开必须报错。拿空密码去连 SMTP,得到的只有一句「认证失败」,
//     把真正的原因(KEY_MASTER_SECRET 换了)盖在下面;
//   - 每个键缺失时回退到 env。Sync 可能整轮失败(它只告警不阻断),
//     那时也要按 env 发信,而不是连一个空 host。
func (s *Snapshot) MailerConfig(ctx context.Context) (mailer.Config, error) {
	password, err := s.Secret(ctx, "mail.password")
	if err != nil {
		return mailer.Config{}, fmt.Errorf("读取 mail.password 失败: %w", err)
	}
	return mailer.Config{
		Transport: s.String(ctx, "mail.transport", s.envStr(func(c *Config) string { return c.Mail.Transport }, "console")),
		From:      s.String(ctx, "mail.from", s.envStr(func(c *Config) string { return c.Mail.From }, "noreply@localhost")),
		FromName:  s.String(ctx, "mail.from_name", s.envStr(func(c *Config) string { return c.Mail.FromName }, "")),
		SMTPHost:  s.String(ctx, "mail.host", s.envStr(func(c *Config) string { return c.Mail.SMTPHost }, "")),
		SMTPPort:  s.Int(ctx, "mail.port", s.envInt(func(c *Config) int { return c.Mail.SMTPPort }, 587)),
		Username:  s.String(ctx, "mail.user", s.envStr(func(c *Config) string { return c.Mail.SMTPUsername }, "")),
		Password:  password,
		UseTLS:    s.Bool(ctx, "mail.tls", s.envBool(func(c *Config) bool { return c.Mail.SMTPTLS }, true)),
	}, nil
}

// envStr/envInt/envBool 取 env 里的同名值当兜底;没装配 Cfg 时用调用方给的默认值。
func (s *Snapshot) envStr(pick func(*Config) string, def string) string {
	if s.cfg == nil {
		return def
	}
	return pick(s.cfg)
}

func (s *Snapshot) envInt(pick func(*Config) int, def int) int {
	if s.cfg == nil {
		return def
	}
	return pick(s.cfg)
}

func (s *Snapshot) envBool(pick func(*Config) bool, def bool) bool {
	if s.cfg == nil {
		return def
	}
	return pick(s.cfg)
}

// Secret 取并解开敏感值(给邮件发送这类真正的消费方)。
//
// 返回的是明文,**调用方不得写进日志、审计或接口响应**。
func (s *Snapshot) Secret(ctx context.Context, key string) (string, error) {
	raw, ok := s.lookup(ctx, key)
	if !ok {
		return "", nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("配置 %s 不是字符串", key)
	}
	return s.unseal(v)
}

// lookup 读一个键的原始 JSON 值。
func (s *Snapshot) lookup(ctx context.Context, key string) (json.RawMessage, bool) {
	s.refresh(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.values[key]
	if !ok || !entry.present {
		return nil, false
	}
	return entry.value, true
}

// refresh 在快照过期时重新整表加载一次。
//
// 加载失败**不清空**:沿用上一轮的值比退回代码默认值强 ——
// 后者正是「改了配置突然全失效」的形状。
func (s *Snapshot) refresh(ctx context.Context) {
	s.mu.RLock()
	fresh := !s.loadedAt.IsZero() && time.Since(s.loadedAt) < s.ttl
	s.mu.RUnlock()
	if fresh {
		return
	}
	s.reload(ctx)
}

func (s *Snapshot) reload(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 双检:并发的 refresh 里已经有人加载过了。
	if !s.loadedAt.IsZero() && time.Since(s.loadedAt) < s.ttl {
		return
	}
	if s.store == nil {
		s.loadedAt = time.Now()
		return
	}
	version := s.version
	list, err := s.store.List(ctx)
	if err != nil {
		s.log.Warn("settings: 读取配置失败,本轮沿用上一轮的值", "error", err)
		s.loadedAt = time.Now()
		return
	}
	// 整表加载期间有人写过 → 这份快照已经过时,作废。
	if s.version != version {
		return
	}
	next := make(map[string]cached, len(list))
	for _, item := range list {
		next[item.Key] = cached{value: item.Value, present: true, updatedAt: item.UpdatedAt}
	}
	s.values = next
	s.loadedAt = time.Now()
}

// ---------------------------------------------------------------- 写

// List 列出全部配置项,**敏感键一律回显掩码**。
func (s *Snapshot) List(ctx context.Context) ([]Setting, error) {
	s.reload(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Setting, 0, len(s.values))
	for key, entry := range s.values {
		if !entry.present {
			continue
		}
		value := entry.value
		if spec, ok := Lookup(key); ok && spec.IsSecret() {
			value = maskJSON(entry.value)
		}
		out = append(out, Setting{Key: key, Value: value, UpdatedAt: entry.updatedAt})
	}
	// map 遍历顺序随机,固定下来免得前端每次刷新表格都在跳。
	sortSettings(out)
	return out, nil
}

// Upsert 校验、加密、写库并就地更新内存。
//
// 与 SettingStore.Upsert 的区别:这里按登记表把关。原先是任意键任意值
// 都能写进去,前端也承认会写出垃圾键。
func (s *Snapshot) Upsert(ctx context.Context, key string, value json.RawMessage, updatedBy *uuid.UUID) (Setting, error) {
	return s.put(ctx, key, value, updatedBy)
}

// ValidateUpdate 校验一批要写入的配置。
//
// **先整体校验,再统一写入**:逐个边写边校验会留下「改了一半」的配置,
// 而半套配置比整套旧配置难查得多。
func (s *Snapshot) ValidateUpdate(ctx context.Context, req map[string]json.RawMessage) error {
	for key, value := range req {
		if _, err := Validate(key, value); err != nil {
			return err
		}
	}

	// 跨键约束:两个键各自都在自己的取值范围内,合起来却可能互相矛盾。
	// 密码上下界一旦颠倒,注册链路会全灭 —— 每个密码都「太长」又「太短」。
	minimum := s.Int(ctx, "password.min_length", 8)
	maximum := s.Int(ctx, "password.max_length", 128)
	if v, ok := intReq(req, "password.min_length"); ok {
		minimum = v
	}
	if v, ok := intReq(req, "password.max_length"); ok {
		maximum = v
	}
	// 跨键约束:邮件配置同理。transport=smtp 却没有服务器或账号,
	// 单看每个键都合法,合起来发信这条路走不通 —— 与 env 共用同一条规则
	// (mail.go),只是叫法不同:这里叫 mail.host,env 里叫 SMTP_HOST。
	transport := s.String(ctx, "mail.transport", s.envStr(func(c *Config) string { return c.Mail.Transport }, "console"))
	from := s.String(ctx, "mail.from", s.envStr(func(c *Config) string { return c.Mail.From }, "noreply@localhost"))
	host := s.String(ctx, "mail.host", s.envStr(func(c *Config) string { return c.Mail.SMTPHost }, ""))
	user := s.String(ctx, "mail.user", s.envStr(func(c *Config) string { return c.Mail.SMTPUsername }, ""))
	if v, ok := strReq(req, "mail.transport"); ok {
		transport = v
	}
	if v, ok := strReq(req, "mail.from"); ok {
		from = v
	}
	if v, ok := strReq(req, "mail.host"); ok {
		host = v
	}
	if v, ok := strReq(req, "mail.user"); ok {
		user = v
	}
	if err := ValidateMailConfig(transport, from, host, user, SettingMailLabels); err != nil {
		return err
	}

	return CheckPasswordBounds(minimum, maximum)
}

func intReq(req map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := req[key]
	if !ok {
		return 0, false
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

func strReq(req map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := req[key]
	if !ok {
		return "", false
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	return v, true
}

// AuditValue 把一次写入翻译成**可进审计**的形式。
//
// 敏感键永远是掩码:审计表会被读、被导出、被展示在后台 ——
// 现在把 SMTP 密码原样写进去,等于给它开了第二条明文流通路径
// (docs/configuration.md §6.4)。
func AuditValue(key string, value json.RawMessage) string {
	if spec, ok := Lookup(key); ok && spec.IsSecret() {
		return MaskValue
	}
	return string(value)
}

// put 是 Upsert 与启动回填共用的写入路径。
func (s *Snapshot) put(ctx context.Context, key string, value json.RawMessage, updatedBy *uuid.UUID) (Setting, error) {
	if s.store == nil {
		return Setting{}, fmt.Errorf("配置存储未装配")
	}
	spec, err := Validate(key, value)
	if err != nil {
		return Setting{}, err
	}

	if spec.IsSecret() {
		plain, err := secretString(value)
		if err != nil {
			return Setting{}, err
		}
		if plain == MaskValue {
			// 「回显掩码」= 没有改。原样保留,既不写库也不审计。
			cur, err := s.currentRaw(ctx, key)
			if err != nil {
				return Setting{}, err
			}
			return Setting{Key: key, Value: cur}, nil
		}
		value, err = s.seal(plain)
		if err != nil {
			return Setting{}, err
		}
	}

	stored, err := s.store.Upsert(ctx, key, value, updatedBy)
	if err != nil {
		return Setting{}, err
	}

	s.mu.Lock()
	s.values[key] = cached{value: stored.Value, present: true, updatedAt: stored.UpdatedAt}
	s.version++
	s.mu.Unlock()
	return stored, nil
}

// currentRaw 取库里当前的原值(掩码场景判断「有没有值」)。
func (s *Snapshot) currentRaw(ctx context.Context, key string) (json.RawMessage, error) {
	if item, err := s.store.Get(ctx, key); err == nil {
		return item.Value, nil
	}
	return json.RawMessage(`""`), nil
}

// SecretString 读取并解开敏感值,缺失返回空串。
//
// 与 Secret 的区别是不返回错误:消费方拿到空串就该退回 env 或关闭该功能。
func (s *Snapshot) SecretString(ctx context.Context, key string) string {
	v, err := s.Secret(ctx, key)
	if err != nil {
		s.log.Warn("settings: 敏感配置解密失败", "key", key, "error", err)
		return ""
	}
	return v
}

func (s *Snapshot) seal(plain string) (json.RawMessage, error) {
	if plain == "" {
		return json.RawMessage(`""`), nil
	}
	if s.sealer == nil {
		return nil, fmt.Errorf("配置加密未装配(KEY_MASTER_SECRET 缺失),拒绝把明文写进配置表")
	}
	enc, err := s.sealer.Seal([]byte(plain))
	if err != nil {
		return nil, fmt.Errorf("加密配置值失败: %w", err)
	}
	out := secretPrefix + base64.RawURLEncoding.EncodeToString(enc)
	b, _ := json.Marshal(out)
	return b, nil
}

func (s *Snapshot) unseal(stored string) (string, error) {
	if stored == "" || !hasSecretPrefix(stored) {
		return stored, nil
	}
	if s.sealer == nil {
		return "", fmt.Errorf("配置加密未装配,无法解密 %s", "敏感配置")
	}
	raw, err := base64.RawURLEncoding.DecodeString(stored[len(secretPrefix):])
	if err != nil {
		return "", fmt.Errorf("配置密文格式非法: %w", err)
	}
	plain, err := s.sealer.Unseal(raw)
	if err != nil {
		return "", fmt.Errorf("配置解密失败,通常是 KEY_MASTER_SECRET 与入库时不一致: %w", err)
	}
	return string(plain), nil
}

const secretPrefix = "enc:v1:"

func hasSecretPrefix(v string) bool {
	return len(v) >= len(secretPrefix) && v[:len(secretPrefix)] == secretPrefix
}

// ---------------------------------------------------------------- 启动回填

// Sync 回填登记表里的键并打印与 env 种子的差异。
//
// 两件事都为同一条纪律服务:**改了 .env 必须有反应,或者必须有提示**。
//
//   - 库里缺行 → 用 env 种子补(否则从旧版本升级上来的库缺键,
//     静默退回代码默认值,又是一次「静默不生效」);
//   - 行还在、但 `updated_by IS NULL`(= 迁移种的默认值,没人改过)且值与
//     env 不同 → 用 env 覆盖。迁移种的是**硬编码默认值**,不是用户的 env,
//     否则 REGISTRATION_MODE=invite_only 的老部署升级后会被表里的 'open' 顶掉;
//   - 行被后台改过(updated_by 非空)→ 一律尊重 setting,env 只当种子
//     (这正是 docs/configuration.md 3 「env 是种子,setting 是现值」的落点)。
func (s *Snapshot) Sync(ctx context.Context) error {
	if s.store == nil || s.cfg == nil {
		return nil
	}
	list, err := s.store.List(ctx)
	if err != nil {
		return fmt.Errorf("读取配置表失败: %w", err)
	}
	current := make(map[string]Setting, len(list))
	for _, item := range list {
		current[item.Key] = item
	}

	for _, spec := range registry {
		if spec.Seed == nil {
			continue
		}
		seed := spec.Seed(s.cfg)
		if seed == nil {
			continue
		}
		row, exists := current[spec.Key]
		switch {
		case !exists:
			if _, err := s.put(ctx, spec.Key, seed, nil); err != nil {
				return fmt.Errorf("回填 %s 失败: %w", spec.Key, err)
			}
			s.log.Info("settings: 回填缺失的键", "key", spec.Key)
		case row.UpdatedBy == nil && !sameJSON(row.Value, seed):
			if _, err := s.put(ctx, spec.Key, seed, nil); err != nil {
				return fmt.Errorf("同步 %s 失败: %w", spec.Key, err)
			}
			s.log.Info("settings: 用 env 种子更新未被改过的键", "key", spec.Key)
		}
	}

	// 与 env 种子不同的键 = 改 .env 不会生效的键。一行日志,
	// 消灭一整类「我改了 .env 怎么没用」的工单。
	var diff []string
	for _, spec := range registry {
		if spec.Seed == nil {
			continue
		}
		seed := spec.Seed(s.cfg)
		if seed == nil {
			continue
		}
		row, exists := current[spec.Key]
		if exists && row.UpdatedBy != nil && !sameJSON(row.Value, seed) {
			diff = append(diff, spec.Key)
		}
	}
	if len(diff) > 0 {
		s.log.Info("settings: 与 env 种子不同的键(改 .env 不会覆盖它们)",
			"count", len(diff), "keys", diff)
	}

	// 回填改了库,内存副本得跟上 —— 而且必须**强制**重读:
	// 回填可能补了整行新键,老快照里压根没有这个键,
	// 按 TTL 的常规刷新路径会因为「刚加载过」直接跳过。
	s.reloadNow(ctx)
	return nil
}

// reloadNow 无视 TTL,立刻重读整表。
func (s *Snapshot) reloadNow(ctx context.Context) {
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
	s.reload(ctx)
}

// sameJSON 比较两段 JSON 是否表达同一个值。
//
// 直接比字节会被格式差异骗到(jsonb 会重新排版),
// 而我们比的是「用户看到的值有没有变」。
func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// maskJSON 把敏感值换成掩码;库里没有值时返回空串。
//
// 掩码的语义是「这里有个值,但接口不告诉你」—— 明文值(例如 env 种子
// 还没被后台重写)同样要遮住,否则 GET 就成了读密码的后门。
func maskJSON(value json.RawMessage) json.RawMessage {
	var v string
	if err := json.Unmarshal(value, &v); err != nil || v == "" {
		return json.RawMessage(`""`)
	}
	b, _ := json.Marshal(MaskValue)
	return b
}

// sortSettings 按键名排序。
//
// map 的遍历顺序是随机的,不排序的话后台表格每次刷新都在跳行,
// 用户根本没法一眼确认「我刚改的那行在哪」。
func sortSettings(list []Setting) {
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
}

func secretString(value json.RawMessage) (string, error) {
	var v string
	if err := json.Unmarshal(value, &v); err != nil {
		return "", fmt.Errorf("敏感配置必须是字符串")
	}
	return v, nil
}

// nopWriter 让默认 logger 真的什么都不做。
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
