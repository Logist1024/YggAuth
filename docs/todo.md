# 待办清单

> 本文件记录**实测过程中发现但还没动手修**的小问题。不是路线图,不是重构计划 —— 是「下次想起来要做」的事。
> 做完一条删一条,做不完就留着,每次迭代走一遍。

---

## 登录与认证

- [ ] **CSRF 拦截与「缺少权限」共用错误码 30001**
  `httpx.CSRFProtect` 失败时返回 `CodeForbidden` +「请求来源校验失败」,而 `RequirePermission` 返回同一个码 +「缺少权限:xxx」。
  同源校验失败与「你没有权限」是两件毫不相干的事,前端只能靠**比对后端文案**来给对提示
  ( `web/shared/src/api.ts` 的 `SAME_ORIGIN_HINT` 特判) —— 后端哪天改了这句话,提示会静默退回成「你没权限」。
  修法:`apperr` 里**追加**一个专用码(现有数值不能改,见 `apperr.go` 的注释),`CSRFProtect` 改用它,
  `web/shared/src/codes.ts` 加一条提示,再删掉 `api.ts` 里那段特判。

- [ ] **`POST /api/auth/email/resend` 实际上没有任何人能调用**
  它挂在 `requireAuth` 下(`internal/identity/handler.go`),而账号一旦进入「待验证邮箱」,
  `internal/transport/auth.go` 的 `acc.Status.Usable()` 检查就会让**所有**带会话的请求回 403 ——
  于是真正需要重发验证邮件的人拿不到接口,拿得到接口的人(邮箱已验证)也不需要它。
  实测:改完邮箱后 `GET /api/account/` 立刻返回 `20003 账号已被禁用`,当前会话从这一刻起就失效了。
  修法(后端):重发接口只校验「会话属于本账号」,不要求账号 `Usable()`;
  或者更彻底一点,像注册那样把 `verify_url` 一并回给调用方,前端直接给按钮。
  前端已按现状兜住:改邮箱后主动把人送回登录页并说清「验证完成后才能重新登录」
  (`web/apps/account/src/views/SecurityView.vue`),没有硬塞一个点不动的「重发邮件」按钮。

---

## 配置与部署

- [ ] **`.env.example` 时长值缺单位**
  `LOGIN_LOCK_SECONDS=900`、`MAIL_VERIFY_COOLDOWN_SECONDS=60` 是纯数字,**启动即退出**。
  应改为 `900s` / `60s`。

- [ ] **`SMTP_USER` vs `SMTP_USERNAME` 键名不一致**
  根 `docker-compose.yml` 与 `.env.example` 用 `SMTP_USERNAME`,应用读 `SMTP_USER`。
  生产编排走 smtp 时启动失败;测试覆盖层已修,生产未修。

- [ ] **compose `environment` 是白名单**
  `.env` 里配了但编排没写的变量在容器里不存在且无提示(`REGISTRATION_MODE`、`LOG_*`、
  `PASSWORD_*`、`SESSION_*` 等),改完需 `docker compose config` 核对。

---

## 前端

- [ ] **`ChangePasswordView` 组件重复**
  `web/apps/account/src/views/ChangePasswordView.vue` 与 `web/apps/admin/src/views/ChangePasswordView.vue`
  内容基本相同,可抽成 `web/shared/src/components/ChangePasswordView.vue`。

- [ ] **站点配置 `site.ts` 未纳入 shared 导出**
  `web/shared/src/site.ts` 未被 `index.ts` re-export,前端各 app 自行导入,跨 app 同步困难。

---

## 安全

- [ ] **HIBP 泄露检查未接线**
  `internal/identity/account/password.go` 预留了 HIBP 接口,但当前未调用。
  这是 C-2「查泄露库」承诺的兑现问题。

---

## 文档

- [ ] **本文档需随代码迭代保持同步**
  改接口同步更新 `docs/api.md`,改 schema 同步更新 `docs/data-model.md`,改安全约束同步更新 `docs/security.md`。

---

## 已完成(从旧 TODOs 迁移)

以下条目已修复,从旧 `docs/TODOs.md` 迁移至此保留记录:

- [x] **首启引导管理员**(docs/configuration.md 5)—— 已实现,见 `internal/bootstrap/bootstrap.go`
- [x] **`REGISTRATION_MODE` 透传** —— 已实现
- [x] **`LOGIN_LOCK_SECONDS` 单位** —— 见上条未修
- [x] **`docs/deployment.md` §5.1 setting 优先级** —— 已按 ADR-012 修正
