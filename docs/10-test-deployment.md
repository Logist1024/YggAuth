# 10 · 测试环境部署

## 一、这份文档解决什么问题

[08-deployment.md](./08-deployment.md) 讲的是**生产形态**:公网域名、Let's Encrypt、
Secure cookie、信任反代头。那套默认值在一台没有域名的测试机上既跑不起来,也不该跑。

测试环境的目标和生产**不一样**:

| | 生产 | 测试 |
|---|---|---|
| 数据 | 丢了就完了 | 随时可删,删了要能重建 |
| 日志 | 结构化 JSON 交给采集 | 人直接读 |
| 失败 | 悄悄失败最危险 | **响亮地失败最好** —— 宁可起不来,不要半坏 |
| 凭据强度 | 生产强度 | 够用即可,但绝不与生产共用 |

所以本文档的重点不是「怎么把容器跑起来」(那在 08 里),而是:

1. 在**没有公网域名**的前提下把服务跑起来;
2. 把「第一个管理员」这件事在测试环境里解决掉;
3. 有一套**能判断部署是否成功**的冒烟清单;
4. 写清楚测试环境与生产环境的**已知差异**,避免「测试通过、上线翻车」。

## 二、选择路线

三条路线,按验证力度递增:

| 路线 | 入口 | 能验证 | 不验证 | 适合 |
|---|---|---|---|---|
| **A 本地进程** | `localhost:3000` | API、OIDC 协议、迁移 | 前端反代、SSO cookie 域 | 改代码时的日常自测 |
| **B 单机 HTTP** | `localhost:3000` | 上面全部 + 容器编排 + 健康检查 | HTTPS、SSO 跨子域 | **默认选它** |
| **C 单机 HTTPS** | 自签证书域名 | 上面全部 + Secure cookie + issuer 一致性 | 真实 CA 信任链 | 验收前跑一遍 |

**先用 B,临交付前用 C 跑一次**。A 只在改代码时用,它绕开了容器,
很多「部署有问题」的症状在 A 里根本不会出现。

路线 A 的做法在 [08-deployment.md](./08-deployment.md) 第九节(本机起 PG、
`make migrate`、`make dev` + `pnpm dev`),本文不重复 —— 它验证的是代码,
不是部署。本文的 B 与 C 才验证部署。

## 三、路线 B:单机测试环境(默认)

### 3.1 前置条件

| 依赖 | 版本 | 检查 |
|---|---|---|
| Docker Engine | ≥ 24 | `docker version` |
| Docker Compose | v2(`docker compose`,无连字符) | `docker compose version` |
| 可用端口 | 3000 | `ss -lntp \| grep 3000` |

不需要本机装 Go、Node、PostgreSQL —— 三样都在镜像里。

### 3.2 准备环境变量

```bash
cd /path/to/YggAuth
cp deploy/test/test.env.example .env.test
```

生成两个必填的随机值:

```bash
# 数据库密码
sed -i "s|^DB_PASSWORD=.*|DB_PASSWORD=$(openssl rand -hex 16)|" .env.test

# 私钥加密主密钥。**换掉它等于库里所有签名私钥作废**,
# 所以测试环境也用真随机值,别图省事写个 123456。
sed -i "s|^KEY_MASTER_SECRET=.*|KEY_MASTER_SECRET=$(openssl rand -hex 32)|" .env.test
```

`.env.test` 放在**仓库根**,因为 `.gitignore` 里的 `*.test` 规则会把它挡住。
换个名字(比如 `test.env`)就没有任何规则匹配,会被 git 收进去 ——
密钥一旦进了历史,删掉文件也删不掉,必须当作已泄露处理。

模板里 `APP_PUBLIC_DOMAIN` / `PUBLIC_BASE_URL` 用的是 `localhost`,
**不要改成 `127.0.0.1`** —— 改了会让 cookie 域推导成 `27.0.0.1`,
也会让 CSRF 的 Origin 校验失败。理由见 9.7。

### 3.3 启动

```bash
docker compose -p yggauth-test \
  --env-file .env.test \
  -f docker-compose.yml \
  -f deploy/test/docker-compose.test.yml \
  up -d --build
```

几个必须理解的点:

- **`-p yggauth-test`** —— compose 项目名。命名卷(`pgdata`、`appdata`)
  会带上项目名前缀,所以测试环境与本机可能存在的生产编排**数据天然隔离**。
  忘了加这个参数,测试环境会直接复用生产卷。
- **两个 `-f`** —— 第二个文件是**覆盖层**,只写与生产的差异。
  根 `docker-compose.yml` 一个字都不用改。
- **`--env-file .env.test`** —— 注意是 `--env-file`,不是 `env_file`(后者是服务级配置)。
  漏了它,compose 会去读根目录的 `.env`,然后因为缺 `DB_PASSWORD` 直接报错退出。

首次构建约 3–5 分钟(前端依赖树最大)。构建完看状态:

```bash
docker compose -p yggauth-test --env-file .env.test \
  -f docker-compose.yml -f deploy/test/docker-compose.test.yml ps
```

`app` 变成 `healthy` 之前会经历一次迁移,`start_period` 给了 40 秒,
探针失败属正常,**不要**在这期间看到 `starting` 就去重启。

### 3.4 冒烟

```bash
# 就绪:查的是数据库,不只是进程存活
curl -s http://localhost:3000/health/ready | head -c 200; echo

# OIDC 发现文档:issuer 必须与 PUBLIC_BASE_URL 完全一致
curl -s http://localhost:3000/oauth/.well-known/openid-configuration | head -c 300; echo

# 前端:终端用户站在 /,管理后台在 /admin
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:3000/
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:3000/admin
```

四条都返回 200,才算部署成功。完整清单见第六节。

### 3.5 日常操作

把长命令收进别名,否则每次都要重打一遍:

```bash
alias yg='docker compose -p yggauth-test --env-file .env.test -f docker-compose.yml -f deploy/test/docker-compose.test.yml'

yg logs -f app      # 实时日志(文本格式,test.env 里 LOG_FORMAT=text)
yg restart app     # 改配置后重启
yg ps              # 看健康状态
yg down            # 停止,保留数据
yg down -v         # ⚠️ 停止并删除数据卷 —— 回到干净状态
```

## 四、路线 C:带 HTTPS 的测试环境

### 什么时候必须用 C

以下三项**只在 HTTPS 下才能验出对错**,用路线 B 的结论不可迁移:

- `SSO_COOKIE_SECURE=true` 时的 cookie 行为;
- `PUBLIC_BASE_URL` 与实际访问地址是否严格一致(不一致时,
  标准 OIDC 客户端会在**验签阶段**拒绝所有令牌,而不是在授权阶段报错);
- `SSO_COOKIE_DOMAIN` 推导出来的父域对不对。

### 4.1 签一份自签证书

```bash
cd /path/to/YggAuth
mkdir -p deploy/test/certs

# 用你打算在浏览器里访问的名字签。CN 与 SAN 必须一致,
# 现代浏览器只认 SAN,不认 CN。
openssl req -x509 -nodes -newkey rsa:2048 -days 30 \
  -keyout deploy/test/certs/privkey.pem \
  -out    deploy/test/certs/fullchain.pem \
  -subj "/CN=auth.test.local" \
  -addext "subjectAltName=DNS:auth.test.local,DNS:admin.test.local"
```

测试机 hosts 加一行(改完记得刷新解析,`sudo killall -HUP mDNSResponder` 或重连网络):

```
127.0.0.1  auth.test.local
127.0.0.1  admin.test.local
```

浏览器会告警证书不受信,点「继续访问」即可 —— 自签证书的正常现象。
**命令行工具不认这个例外**,`curl` 需要加 `-k`,
OIDC 客户端测试则需要把 CA 装进信任库。

`.env.test` 之外,这一段命令还改了 hosts 与 `deploy/test/certs/`。
私钥落在仓库目录下看着吓人,但 `.gitignore` 里的 `*.pem` 会把它挡住 ——
证书目录可以放心放在这里。

### 4.2 改 `.env.test`

```bash
APP_PUBLIC_DOMAIN=auth.test.local
PUBLIC_BASE_URL=https://auth.test.local
SSO_COOKIE_SECURE=true
SSO_COOKIE_DOMAIN=          # 留空,由 APP_PUBLIC_DOMAIN 推导
```

`SSO_COOKIE_DOMAIN` **留空**。显式写成 `.test.local` 也行,
但写成 `localhost` 会被启动校验直接拒绝 —— localhost 没有父域。

### 4.3 启动

```bash
# --profile tls 才会拉起 nginx(覆盖层里给它设了 profiles 门控)
docker compose -p yggauth-test --env-file .env.test \
  -f docker-compose.yml -f deploy/test/docker-compose.test.yml \
  --profile tls up -d
```

nginx 的宿主端口来自根编排的 `HTTP_PORT` / `HTTPS_PORT`,默认是 80 / 443。
被占用时在 `.env.test` 里改掉(该端口是**宿主机**端口,容器内仍是 80 / 443):

```bash
HTTP_PORT=8080
HTTPS_PORT=8443
```

访问 `https://auth.test.local:8443/`。端口变了,`PUBLIC_BASE_URL` 也要跟着改 ——
**issuer 与实际访问地址不一致时,标准 OIDC 客户端是在验签阶段拒绝令牌,
不是报错**,这类问题极难定位。

### 4.4 四处差异必须改回去

测试配置是为了「验证方便」牺牲了下面几处安全性,**切生产前必须还原**:

| 位置 | 测试环境 | 生产要求 |
|---|---|---|
| `TRUST_PROXY_HEADERS` | `false` | `true`(nginx 在前面,真实 IP 靠它) |
| nginx `server {}` | HTTP 不跳 301 | HTTP 只留 ACME,其余 301 到 HTTPS |
| nginx OCSP stapling | 关 | 开 |
| HSTS `max-age` | 31536000 | 31536000 + `includeSubDomains` |

## 五、第一个管理员

迁移只种了 `platform_admin` 与 `user` 两个**角色**,
**没有**任何账号被授予它们(见 `db/migrations/00001_init_identity.sql` 末尾的种子数据)。
所以测试环境的第一个管理员要手工授一次。

### 5.1 先注册一个账号

```bash
# ⚠️ /api/* 的写操作会过 CSRF 校验:必须带 Origin 或 Referer,
# 两者都没有会被拒(httpx.CheckSameOrigin)。curl 默认两个都不带。
curl -s -X POST http://localhost:3000/api/auth/register \
  -H 'Content-Type: application/json' \
  -H "Origin: http://localhost:3000" \
  -d '{"username":"tester","email":"tester@test.local","password":"correct-horse-battery"}'
```

注册这一刻后端就会发验证邮件;`MAILER_TRANSPORT=console` 时信只进日志、到不了收件人,
所以响应里**额外**带一个 `verify_url` 让本地闭环跑得通;`smtp` 部署则只回 `account`
—— 令牌只从邮件那一条路出去(见 `account.Config.HideVerifyURL`):

```json
{"code":0,"message":"ok","data":{"account":{...},"verify_url":"http://localhost:3000/verify-email?token=..."}}
```

### 5.2 激活并授予管理员

两种做法,任选其一。

**做法 A —— 走正常流程**(能顺带验证邮箱链路):

```bash
TOKEN=$(curl -s -X POST http://localhost:3000/api/auth/register \
  -H 'Content-Type: application/json' -H "Origin: http://localhost:3000" \
  -d '{"username":"tester2","email":"tester2@test.local","password":"correct-horse-battery"}' \
  | sed -n 's/.*token=\([^"]*\)".*/\1/p')

curl -s -X POST http://localhost:3000/api/auth/email/verify \
  -H 'Content-Type: application/json' -H "Origin: http://localhost:3000" \
  -d "{\"token\":\"$TOKEN\"}"
```

**做法 B —— 直接改库**(测试环境推荐,快且不依赖邮件):

```bash
# -T 关闭 TTY 分配:有 heredoc 管道时必须加,否则 psql 拿到的是终端而不是 SQL。
# -U / -d 从容器自身的环境变量取,改过 DB_NAME / DB_USER 也不用改命令。
docker compose -p yggauth-test --env-file .env.test \
  -f docker-compose.yml -f deploy/test/docker-compose.test.yml \
  exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'SQL'
-- 激活账号。status 只有 active 才允许登录,
-- 刚注册时是 pending_verification。
UPDATE identity.account
   SET status = 'active', email_verified_at = now()
 WHERE lower(email) = 'tester@test.local';

-- 授予平台管理员。ON CONFLICT 让这段可以反复执行。
INSERT INTO identity.account_role (account_id, role_id)
SELECT a.id, r.id
  FROM identity.account a, identity.role r
 WHERE lower(a.email) = 'tester@test.local' AND r.code = 'platform_admin'
ON CONFLICT DO NOTHING;

-- 顺便看看结果。应该看到 17 个权限点。
SELECT count(*) AS permissions
  FROM identity.account_role ar
  JOIN identity.role_permission rp ON rp.role_id = ar.role_id
 WHERE ar.account_id = (SELECT id FROM identity.account WHERE lower(email) = 'tester@test.local');
SQL
```

### 5.3 验证

```bash
# 登录,拿到会话 cookie
curl -s -c /tmp/yg.cookie -X POST http://localhost:3000/api/auth/login \
  -H 'Content-Type: application/json' -H "Origin: http://localhost:3000" \
  -d '{"email":"tester@test.local","password":"correct-horse-battery"}'

# 后台接口认这个 cookie
curl -s -b /tmp/yg.cookie http://localhost:3000/api/admin/menus | head -c 300; echo
```

`/api/admin/menus` 返回菜单树即成功。返回 403 说明权限没授上,
回 5.2 再查一次。

## 六、冒烟测试清单

按顺序跑。每一项都写明了**期望值**,不要只看「有没有报错」——
测试环境最常见的失败模式是**静默失败**(SSO 失效、CSRF 静默拒绝)。

| # | 检查项 | 命令 | 期望 |
|---|---|---|---|
| 1 | 进程存活 | `curl -s localhost:3000/health/live` | 200,且不查数据库 |
| 2 | 数据库可达 | `curl -s localhost:3000/health/ready` | 200;数据库挂掉时必须 503 |
| 3 | 迁移到位 | `docker compose ... exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c '\dt identity.*'` | 10 张表(`account` … `audit_event`)全部存在 |
| 4 | OIDC issuer | `curl -s localhost:3000/oauth/.well-known/openid-configuration` | `issuer` 与 `PUBLIC_BASE_URL` **逐字符相同** |
| 5 | JWKS | `curl -s localhost:3000/oauth/.well-known/jwks.json` | 含至少一个公钥 |
| 6 | 密码策略 | `curl -s localhost:3000/api/auth/policy` | `data.password_min_length=8`、`data.password_max_length=128` |
| 7 | 注册 | 见 5.1 | 201;console 部署响应含 `verify_url`,`smtp` 只回 `account` |
| 8 | 登录 | 见 5.3 | 200,响应体是 `{"code":0,...}` 信封 |
| 9 | 会话生效 | `curl -s -b /tmp/yg.cookie localhost:3000/api/account/` | 200,返回账号信息 |
| 10 | 后台可达 | `curl -s -b /tmp/yg.cookie localhost:3000/api/admin/me` | 200,含权限点 |
| 11 | 未登录被拒 | `curl -s localhost:3000/api/account/` | 401,**不是** 200 也不是 500 |
| 12 | 终端用户站 | 浏览器打开 `/` | 登录页能渲染(不是白屏) |
| 13 | 管理后台 | 浏览器打开 `/admin` | 能进后台,菜单非空 |
| 14 | metrics | `curl -s localhost:3000/metrics` | 200(Prometheus 文本) |

第 14 项**走 nginx 时应当是 404** —— 站点配置里显式拒绝了对公网暴露 metrics。
在测试环境要看它,直连 app 端口,不要为此改站点配置。

## 七、重置与并存

### 7.1 回到干净状态

```bash
# ⚠️ -v 会删除命名卷:账号、会话、皮肤文件、迁移记录全部消失。
docker compose -p yggauth-test --env-file .env.test \
  -f docker-compose.yml -f deploy/test/docker-compose.test.yml down -v
```

验证某个可疑问题时,**先重置再复现**。带着脏数据测出来的现象往往不可复现。

### 7.2 与本机生产编排并存

两套编排可以同时跑,前提是**项目名不同、端口不同**:

```bash
# 测试
docker compose -p yggauth-test --env-file .env.test ... up -d   # 3000
# 生产(本机演练)
docker compose -p yggauth-prod --env-file .env        ... up -d  # 80/443
```

命名卷按项目名隔离,`pgdata` / `appdata` 不会串。
唯一共享的是 `./db/migrations` 的只读挂载 —— 它是同一份代码,共享是对的。

### 7.3 不要做的事

| 别做 | 原因 |
|---|---|
| 测试与生产**共用** `KEY_MASTER_SECRET` | 一旦泄露,两个环境的签名私钥一起报废 |
| 测试与生产共用 `DB_PASSWORD` | 测试环境的弱口令就是生产的后门 |
| 测试环境连生产数据库 | 一条 `DROP` 就没有回头路 |
| 把 `.env.test` 改名为 `test.env` 提交 | 挡住它的是 `.gitignore` 的 `*.test` 规则,换个名字就没有任何规则匹配 |

## 八、测试环境专用配置

只列与生产**有意不同**的项。其余取 `internal/config/config.go` 的默认值。

| 变量 | 测试值 | 生产值 | 为什么 |
|---|---|---|---|
| `LOG_FORMAT` | `text` | `json` | 人直接读日志 |
| `LOG_ADD_SOURCE` | `true` | `false` | 排障时定位到行号 |
| `LOG_REQUEST_BODY` | `false` | `false` | 打开会把**密码明文**写进日志,两边都不能开 |
| `TRUST_PROXY_HEADERS` | `false` | `true` | 直连时没有可信反代,开着等于让任何人伪造来源 IP |
| `SSO_COOKIE_SECURE` | `false`(路线 B) | `true` | 与协议保持一致,否则浏览器直接丢 cookie |
| `MAILER_TRANSPORT` | `console` | `smtp` | 验证链接直接在日志里 |
| `MC_READONLY` | `true`(多人共用时) | `false` | 防止互相覆盖材质 |

`LOG_REQUEST_BODY` 那一行值得单独强调:它是唯一一个**测试环境也不该打开**的开关。
测试数据里往往包含真实用户信息,「只是测试环境」不是打开它的理由。

## 九、已知坑

以下都是**仓库现状**导致的,不是配置写错。踩到时不必怀疑自己的 `.env`。

### 9.1 `SMTP_USER` 与 `SMTP_USERNAME` 对不上

应用读的是 `SMTP_USER`(`internal/config/config.go` 的 `Load()`),
而根 `docker-compose.yml` 透传的是 `SMTP_USERNAME`,`.env.example` 里写的也是 `SMTP_USERNAME`。

后果:`MAILER_TRANSPORT=smtp` 时,应用在容器里读到的用户名永远是空,
启动校验直接失败:

```
配置错误: SMTP_USER 未设置(MAILER_TRANSPORT=smtp 时必填)
```

测试覆盖层里已经补了 `SMTP_USER` 的透传,所以路线 B/C 走 smtp 时是通的。
**生产编排仍有这个问题**,需要把根 compose 与 `.env.example` 统一成 `SMTP_USER`。

### 9.2 根编排不透传大部分配置项

compose 的 `environment` 是**白名单**语义:`.env` 里配了、但编排没写的变量,
容器里就是不存在,应用只能吃默认值,而且**没有任何提示**。

最常被误配的几个:`REGISTRATION_MODE`、`LOG_LEVEL`、`LOG_FORMAT`、`PASSWORD_*`、
`SESSION_*`、`MC_READONLY`、`OIDC_*_TTL`。例如把 `.env` 里的
`REGISTRATION_MODE=invite_only` 删掉并不会报错,只是注册重新变成开放状态。

覆盖层 `deploy/test/docker-compose.test.yml` 已经把这些补齐。
**判据很简单:改完配置先 `docker compose config` 看一眼渲染结果**,
或者直接进容器 `env | sort` 确认。

### 9.3 `ADMIN_DOMAIN` 目前不参与路由

它在 `config.Load()` 里被读进来,只在启动日志里出现,没有任何逻辑使用它。
管理后台**固定**挂在 `/admin` 前缀下,与 `ADMIN_DOMAIN` 无关。

所以测试环境访问后台一律是 `http://localhost:3000/admin`,不要去找
`admin.test.local` —— 配了也不会生效。

### 9.4 明文 HTTP + Secure cookie 会直接拒绝启动

```
配置错误: PUBLIC_BASE_URL 是 http 但 SSO_COOKIE_SECURE=true,
cookie 会被浏览器丢弃;本地调试请设 SSO_COOKIE_SECURE=false
```

这是**好事**:启动期拦住,而不是等到 SSO 静默发码失效再排查。
豁免只覆盖 `localhost` / `127.0.0.1` / `::1` / `*.local`。

反向的坑更隐蔽:`SSO_COOKIE_DOMAIN` 写成 `localhost` 同样会被拒(无父域)。
留空让应用从 `APP_PUBLIC_DOMAIN` 推导即可。

### 9.5 头像首次请求返回 404

头像渲染是**异步**的,首次请求时结果还没落盘,返回默认头像属正常。
重试一次即可,不要据此判定部署失败。

### 9.6 泄露密码检查目前没有接线

`config.Load()` 读出了 `HIBP_ENDPOINT`,但 `wire.go` 里从头到尾**没有给
`account.Config.LeakChecker` 赋值**。它是接口字段,为 nil 时
`service.Register` 直接跳过检查。

也就是说:

- `.env.example` 里写的「留空则跳过泄露检查(离线环境)」**与实际行为对不上** ——
  留空不会「跳过」,而是回落到默认地址;而无论填什么都不检查,因为检查器不存在。
- 想在测试环境验证弱密码拦截,得先把 `LeakChecker` 的实现装进去,
  否则改 `PASSWORD_REJECT_COMMON` 也只影响内置弱口令表那部分。

这条属于**已知的功能缺口**,不是配置错误。测试时别把它当成「已生效的安全项」。

### 9.7 `localhost` 与 `127.0.0.1` 不能混用

这是本指南所有 curl 示例都写 `http://localhost:3000` 的原因,两个独立的坑:

**其一,CSRF 的 Origin 必须与 Host 逐字符相同。**
`httpx.CheckSameOrigin` 拿 `Origin` 的 host 和请求的 `Host` 比,
不一致直接 403「请求来源校验失败」。用 `127.0.0.1:3000` 发请求、
带 `Origin: http://localhost:3000`,必定被拒 —— 而错误信息里
完全看不出是「你把地址写混了」。

**其二,`APP_PUBLIC_DOMAIN=127.0.0.1` 会推导出错误的 cookie 域。**
`wire.go` 的 `cookieDomain()` 取第一个 `.` 之后的部分当父域,
`127.0.0.1` 推导出的结果是 `27.0.0.1`。浏览器收到 `Domain=27.0.0.1`
会直接丢弃 cookie,而应用侧看不到任何错误。`localhost` 走的是显式特判,
返回空值,是唯一安全的本机取值。

顺带一个反向的坑:覆盖层把端口绑在 `127.0.0.1` 上,
而部分系统把 `localhost` 解析到 `::1` 优先。此时 curl 会报连接被拒。
**解决办法是放开绑定地址**(`0.0.0.0`),不要改成用 `127.0.0.1` 访问 ——
那会撞上上面第一条。

### 9.8 迁移目录的只读挂载

根编排把 `./db/migrations` 以 `:ro` 挂进容器,应用启动时执行 `goose up`。
应用只读、从不写入,所以只读是对的。

绑定挂载是实时的:改了仓库里的迁移文件,容器里立刻看得到。
但迁移**只在启动时执行**,所以改完要 `restart app` 才会应用 ——
只改文件不起作用,是这里最容易误判的一点。

## 十、测试通过 ≠ 可以上线

验收前对着这张表过一遍。每一项都对应一个「测试环境测不出来」的失败模式。

| 检查 | 怎么确认 |
|---|---|
| `PUBLIC_BASE_URL` 与真实访问地址**逐字符**一致 | `curl $PUBLIC_BASE_URL/oauth/.well-known/openid-configuration \| grep issuer` |
| `KEY_MASTER_SECRET` 是新的随机值,不是从 `.env.test` 抄的 | `openssl rand -hex 32` 对一遍长度(64 hex) |
| `SSO_COOKIE_SECURE=true` | [09-security.md](./09-security.md) 上线检查清单 |
| `TRUST_PROXY_HEADERS=true` | nginx 在前面时才可为 true |
| `LOG_REQUEST_BODY=false` | grep 一次 `.env`,比肉眼可靠 |
| `MAILER_TRANSPORT=smtp` 且四项齐全 | 用 9.1 的方式确认键名 |
| `MC_SERVER_SHARED_SECRET` 已设置 | 留空则必须在后台登记 MC 服务器 |
| 迁移在预发验证过,含破坏性变更的 `Down` | `docker compose ... exec app /usr/local/bin/yggauth migrate -to status` |
| 回滚方案写下来了 | `migrate -to down` 只回退**一步** |

最后一条:测试环境数据可以随时删,生产不能。
上线前把「回滚到什么状态」和「谁来执行」写清楚,比事后想强。

## 十一、跑一遍仓库自带的部署自检

仓库里有两个包专门盯部署配置,改过编排或 `.env` 之后跑一下:

```bash
# 静态验证 compose / Dockerfile / nginx 配置的自洽性,不需要 Docker daemon
go test -tags=integration -run 'TestCompose|TestDockerfile|TestNginx' ./internal/deploycheck/ ./internal/security/

# 全部集成测试(含 embedded-postgres,首次会下载 PostgreSQL 二进制)
make test-integration
```

它们能抓到的问题:compose 引用了不存在的环境变量、必填项没用
`${VAR:?提示}` 的 fail-fast 语法、绑定挂载指向不存在的目录、
`COPY --from` 指向不存在的构建阶段、`service_healthy` 依赖了没有
healthcheck 的服务、站点配置把 `/metrics` 放行、文档出现悬空链接。

这几个测试不跑,上面一半的部署问题要等到 `docker compose up` 失败时才发现。

---

**上一篇**:[09-security.md](./09-security.md) —— 安全设计
**回到**:[00-overview.md](./00-overview.md) —— 项目总纲