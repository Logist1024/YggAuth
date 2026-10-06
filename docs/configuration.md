# 配置方案

> env 是首次启动的种子,setting 表是运行时现值。本文件描述三层配置模型、首启引导、热更新与公开配置端点。

---

## 一、配置分层模型(ADR-012)

```
L0: .env / 环境变量  ←  运维部署时写入
  ↓ Sync (仅当该行不存在时)
L1: app.setting 表  ←  后台运营时可修改
```

**规则**:
1. env 空值**不种行**:防止一个非法零值让整轮 Sync 失败
2. 一旦 setting 有值(`updated_by` 非空),env 不再覆盖它
3. 两边不一致时,启动日志打出「与 env 种子不同的键」提醒人工确认(只打印键名,不打印值)

**两类键**:
- 基础设施项(`DB_HOST`、`KEY_MASTER_SECRET`)归运维,通常在 env 里,不改
- 业务开关(`registration.mode`、`mail.*`)归运营,通过后台修改

---

## 二、唯一真源

`.env.example` 是唯一真源。`internal/config/envfiles_test.go` 双向核对:少一行或多一行都会让测试失败。

**不要**在文档里手抄全表 —— 引用 `.env.example` 即可。本文只讲模型、语义、易错点。

---

## 三、键注册表

新键必须登记在 `internal/config/keys.go` 的注册表里,否则 `config.Load()` 启动即退出并指出缺哪个键。

**登记规则只写一次**(见 `internal/config/keys.go`):

```go
// 示例:注册一个字符串键
AddString("REGISTRATION_MODE", "open", "open | closed | invite_only")

// 示例:注册一个布尔键
AddBool("MC_LOGIN_DEFAULT", true, "新账号默认是否开放 MC 登录")
```

---

## 四、首启引导管理员

M7 引入的首启引导流程:

1. 服务首次启动(`app.setting` 为空)
2. 自动生成随机初始密码(只打印一次到启动日志,见 docs/security.md 6.3)
3. 创建首个账号 `tester@test.local`(状态 `active`),授予 `platform_admin` 角色(17 个权限点)
4. 首次登录后强制改密(`must_change_password = true`)

**迁移文件**:`db/migrations/00008_bootstrap_must_change.sql`。

**手工入口**:`yggauth admin create <email>` 子命令(见 `cmd/yggauth/admin.go`)。

---

## 五、公开配置端点

`GET /api/public/config` 返回站点公开信息(不需要登录):

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "site_name": "YggAuth",
    "registration_mode": "open",
    "require_invite_code": false,
    "mc_login_enabled": true,
    "brand": { "title": "YggAuth", "primary": "#3b6ea5" }
  }
}
```

前端用它决定页面标题、品牌色、注册入口是否可见(见 `web/shared/src/site.ts`)。

---

## 六、运行时配置读取

业务代码通过 `Settings` 接口读取配置(见 `internal/config/settings.go`):

```go
// 读取字符串
mode := settings.GetString("registration.mode")

// 读取布尔
mcLogin := settings.GetBool("mc_login.default")

// 读取时长
cooldown := settings.GetDuration("mail.verify.cooldown")
```

**不直接从 env 读**:业务层通过 `Settings` 接口,保证后台修改立即生效(见 docs/configuration.md 第七节热更新)。

---

## 七、热更新

**发件器热替换**(见 `internal/platform/mailer/hot.go`):
- SMTP 配置修改后,后台触发热替换,无需重启服务
- 替换过程:新建发件器 → 原子切换指针 → 旧发件器关闭

**Settings 热替换**:
- 修改 `app.setting` 表后,内存缓存自动失效
- 下次 `settings.Get*()` 读取新值

**测试邮件**(见 `internal/admin/handler.go`):
- `POST /api/admin/mail/test` 用已保存的生效配置发一封测试信
- 返回阶段化结果(`TestResult`):SMTP 连接 → 发送邮件 → 投递成功/失败

---

## 八、Compose env_file 加载

**这是核心改动**:compose 必须用 `env_file` 整份加载 `.env`,不能逐条手抄 `environment`。

```yaml
services:
  app:
    env_file:
      - .env
```

原因:逐条手抄 `environment` 必然漂移(见 `internal/config/envfiles_test.go` 守的断言 D2/D3)。

---

## 九、易错点

| 坑 | 现象 | 修法 |
|---|---|---|
| 时长值缺单位 | `LOGIN_LOCK_SECONDS=900` 启动即退出 | 改为 `LOGIN_LOCK_SECONDS=900s` |
| SMTP 键名不一致 | `SMTP_USER` vs `SMTP_USERNAME` | 以代码为准,`.env.example` 同步 |
| `ADMIN_DOMAIN` 不参与路由 | 后台固定挂在 `/admin` 前缀 | 不设即可,留空表示与终端用户站同域 |
| `PUBLIC_BASE_URL` 与 issuer 不一致 | OIDC 客户端验签失败 | 必须与访问域名完全一致(含协议) |

---

## 十、黄金路径

`make golden-path` 执行端到端冒烟测试(见 `deploy/test/golden-path.sh`):

1. 容器启动 → 健康检查通过
2. 首启引导 → 首个管理员自动创建
3. 登录 → 获取会话
4. 注册 → 验证邮箱
5. OIDC 授权码流程
6. MC 认证流程
7. 皮肤上传与下载

真容器 1/2/7 步需要 Docker(见 docs/deployment.md 黄金路径章节)。
