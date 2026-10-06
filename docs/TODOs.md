# 待办与小缺陷清单

> 这一份专门用来记录**实测过程中发现但还没动手修**的小问题。
> 不是路线图，不是重构计划 —— 是「下次想起来要做」的事。
> 做完一条删一条，做不完就留着，每次迭代走一遍。

---

## 登录与认证

- [ ] **CSRF 拦截与「缺少权限」共用错误码 30001**
  `httpx.CSRFProtect` 失败时返回 `CodeForbidden` +「请求来源校验失败」，而 `RequirePermission` 返回同一个码 +「缺少权限:xxx」。
  同源校验失败与「你没有权限」是两件毫不相干的事，前端只能靠**比对后端文案**来给对提示
  （`web/shared/src/api.ts` 的 `SAME_ORIGIN_HINT` 特判）—— 后端哪天改了这句话，提示会静默退回成「你没权限」。
  修法：`apperr` 里**追加**一个专用码（现有数值不能改，见 `apperr.go` 的注释），`CSRFProtect` 改用它，
  `web/shared/src/codes.ts` 加一条提示，再删掉 `api.ts` 里那段特判。

- [ ] **生产没有自动 bootstrap 管理员账号**
  现状：迁移只种 `platform_admin` / `user` 两个**角色**，第一个管理员账号要手工注册 + 改库授权（`docs/10-test-deployment.md` 五），新部署第一次启动后什么都做不了。
  建议方案（用户提出）：
  - 系统首次启动时自动创建一个默认管理员
    - 邮箱：`admin@<APP_PUBLIC_DOMAIN>`
    - 用户名：`admin`
    - 密码：默认 `123456`，**仅当请求来源是本机（loopback / unix socket）时允许用默认密码登录**
  - 检测到非本地访问尝试用默认密码登录时，返回明确提示「请通过环境变量 `PASSWORD` 设置管理员密码」
  - 环境变量 `PASSWORD` 若已设置，则用其作为默认管理员密码（覆盖 123456）
  - 风险点：自动创建意味着应用要有「初始化」入口；要在 `REGISTRATION_MODE=invite_only` 时也能跑通（不然公开注册也能创建一个同名账号冲突）；要在启动日志里大声说一句「默认管理员已创建，密码是 xxx」

- [ ] **`POST /api/auth/email/resend` 实际上没有任何人能调用**
  它挂在 `requireAuth` 下（`internal/identity/handler.go`），而账号一旦进入「待验证邮箱」，
  `internal/transport/auth.go` 的 `acc.Status.Usable()` 检查就会让**所有**带会话的请求回 403 ——
  于是真正需要重发验证邮件的人拿不到接口，拿得到接口的人（邮箱已验证）也不需要它。
  实测：改完邮箱后 `GET /api/account/` 立刻返回 `20003 账号已被禁用`，当前会话从这一刻起就失效了。
  修法（后端）：重发接口只校验「会话属于本账号」，不要求账号 `Usable()`；
  或者更彻底一点，像注册那样把 `verify_url` 一并回给调用方，前端直接给按钮。
  前端已按现状兜住：改邮箱后主动把人送回登录页并说清「验证完成后才能重新登录」
  （`web/apps/account/src/views/SecurityView.vue`），没有硬塞一个点不动的「重发邮件」按钮。

## 编排与环境

- [ ] **`REGISTRATION_MODE` 未被 `docker-compose.yml` 透传**
  容器里读不到这个变量，永远吃代码默认值 `open`。要按项目约定同时改 `.env.example` 与文档（`docs/08-deployment.md` / `01-roadmap.md`）。

- [ ] **`LOGIN_LOCK_SECONDS` 命名/解析不一致**（仓库侧缺陷，本地已确认但未提交）
  文档和 `.env.example` 把它当「整数秒」写（`900`），但代码按 Go `time.Duration` 解析。生产环境若有人按文档填 `900`，会解析成 900 纳秒直接 panic。
  修法二选一：
  - 变量改名 `LOGIN_LOCK_DURATION`，文档与 `.env.example` 都写 `15m`
  - 或者保留秒单位，代码里 `.Seconds()` 后转 `time.Duration`
  顺便对齐 `.env.example` 里所有 `*_SECONDS` 类变量。

- [ ] **`SSO_COOKIE_DOMAIN=logist.eu.cc` 显式设置存在脆弱性**
  当前注释说「显式指定，不依赖自动推导」，避免推出 `.eu.cc`。但只要浏览器将来把某个二级域加入公共后缀列表（PSL），显式 Domain 仍可能被拒。长期更稳的做法：单域名部署把 `SSO_COOKIE_DOMAIN` 留空，让 cookie 精确锚定到当前主机（无子域也用不到 Domain）。

## CI 与本地校验

- [ ] **CI 三个盲区**（`make verify` 能抓到，本地容易忘）：
  1. `.github/workflows/ci.yml` 不构建 Docker 镜像 —— 改 Dockerfile 后只跑 Go 测试，构建错误漏到部署才炸
  2. 不跑前端 `pnpm -r typecheck` —— 改 `web/shared` 后类型漂移漏到生产
  3. `internal/deploycheck` 只校验 `COPY --from=` 阶段名，不校验普通 `COPY` 源文件存在 —— 错路径漏到构建才报错
  建议在 CI 里加一条 `docker build --target build .`，以及 `pnpm -r typecheck`。

## 已知功能缺口

（原「后台没有『把角色授予账号』的界面」一条已完成，按约定删除。）

## 部署运维

- [ ] **`yggauth-test` 还是旧镜像**
  绑 `127.0.0.1:3000`，不影响对外站点，但代码已经不同。要刷的话：`docker compose -p yggauth-test --env-file .env.test -f docker-compose.yml -f deploy/test/docker-compose.test.yml up -d --build`（先停旧容器再起，免得 939MB 内存构建时被压垮）。

- [ ] **服务器 `~/YggAuth/.pnpm-store` 与部分 dist 资产是 root 属主**
  早先 `sudo pnpm build` 留下的。`sudo chown -R admin:admin /home/admin/YggAuth` 可修，但治本是以后别在服务器上 `sudo` 跑前端构建。已经在 `docs/08-deployment.md` 标注过但容易忘。

---

## 维护说明

- 每修完一条，从列表里删掉，并在 commit message 里引用原条目编号（如果方便）。
- 新发现的缺陷直接追加到对应小节末尾，不要为了分类另开文件 —— 这一份的目标是「一眼能看完」。
