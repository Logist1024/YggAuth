# 待办与小缺陷清单

> 这一份专门用来记录**实测过程中发现但还没动手修**的小问题。
> 不是路线图，不是重构计划 —— 是「下次想起来要做」的事。
> 做完一条删一条，做不完就留着，每次迭代走一遍。

---

## 登录与认证

- [ ] **`requiresLogin` 太宽，把密码错当成会话过期**（`web/shared/src/api.ts` `ApiError.requiresLogin`）
  现在定义为 `status === 401 || code === 20012`。问题是 `CodeInvalidPassword`（密码错，20002）的 HTTP 状态也是 401，于是账号站登录页填错密码会被前端当成「需要刷新会话」，触发自动跳登录。
  修复方向：改成 `status === 401 && code !== CodeInvalidPassword && code !== CodeAccountLocked`，或者更窄的语义匹配。

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

- [ ] **后台没有「把角色授予账号」的界面**
  接口 `/api/admin/roles/grant` 已存在，但后台缺一个页面调它。新建的角色目前没法分配给任何账号，只能走 SQL 手工 `INSERT`。

## 部署运维

- [ ] **`yggauth-test` 还是旧镜像**
  绑 `127.0.0.1:3000`，不影响对外站点，但代码已经不同。要刷的话：`docker compose -p yggauth-test --env-file .env.test -f docker-compose.yml -f deploy/test/docker-compose.test.yml up -d --build`（先停旧容器再起，免得 939MB 内存构建时被压垮）。

- [ ] **服务器 `~/YggAuth/.pnpm-store` 与部分 dist 资产是 root 属主**
  早先 `sudo pnpm build` 留下的。`sudo chown -R admin:admin /home/admin/YggAuth` 可修，但治本是以后别在服务器上 `sudo` 跑前端构建。已经在 `docs/08-deployment.md` 标注过但容易忘。

---

## 维护说明

- 每修完一条，从列表里删掉，并在 commit message 里引用原条目编号（如果方便）。
- 新发现的缺陷直接追加到对应小节末尾，不要为了分类另开文件 —— 这一份的目标是「一眼能看完」。
