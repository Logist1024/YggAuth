# 06 · Minecraft 域设计与协议适配

> 本文件描述 Yggdrasil 协议的实现要点与皮肤站设计。MC 域是重构中**兼容风险最高**的部分,实现时须对照本文件逐条核对。

## 一、整体定位

MC 服务端通过 **authlib-injector**(一个 mod)把官方 Mojang 认证地址替换为本服务,从而实现「用 YggAuth 的账号进服」。

```
MC 服务端 + authlib-injector
        │
        │  (把官方 api.mojang.com 指向 YggAuth)
        ▼
    YggAuth /mc/*  (Yggdrasil 协议实现)
```

**关键约束**:Yggdrasil 是 Mojang 的**私有协议**,无官方文档。实现依据是 authlib-injector 的源码行为与社区逆向结论。协议细节可能随 authlib-injector 版本变化,需在升级时回归测试。

## 二、认证流程

### 2.1 首次登录(authenticate)

```
客户端                      YggAuth                     账号内核
  │  POST /mc/authenticate   │                            │
  │  {username, password}    │                            │
  │─────────────────────────>│  按用户名查 MC 档案         │
  │                          │────────────────────────────>│
  │                          │  查到 account_id           │
  │                          │<────────────────────────────│
  │                          │  校验 mc_login_enabled      │
  │                          │  argon2id 校验密码          │
  │                          │  签发 MC 访问令牌           │
  │<─────────────────────────│                            │
  │  {accessToken, clientToken, selectedProfile:{id,name}} │
```

**请求**
```json
POST /mc/authenticate
Content-Type: application/json
{
  "agent": { "name": "Minecraft", "version": 1 },
  "username": "Player_One",
  "password": "correct-horse-battery",
  "clientToken": "客户端生成的随机串",
  "requestUser": true
}
```

**响应**
```json
{
  "accessToken": "<UUID 格式的令牌>",
  "clientToken": "客户端生成的随机串",
  "selectedProfile": { "id": "<32位十六进制UUID无横线>", "name": "Player_One" },
  "availableProfiles": [{ "id": "...", "name": "Player_One" }]
}
```

**约束**:
- 令牌格式必须是 **无横线的小写 UUID 格式**(32 位十六进制),MC 服务端会解析
- 令牌存 hash,库里存 `sha256(token)`
- 若 `mc_login_enabled = false`,返回 **403 + code 50004**

### 2.2 进服校验(hasJoined)—— 安全核心

这是协议里**最关键**的校验。MC 服务端进服前调用它,验证两件事:
1. 该玩家确实在本服务有有效会话
2. 调用方**真的是**本服务签发的 MC 服务器(而非伪造的)

**请求**
```
GET /mc/hasJoined?username=Player_One&serverId=<serverId>&ip=<可选>
```

**校验算法(五生效点)**:
```
expected = hex(sha1(serverId + sharedSecret + uuid))
```

其中 `sharedSecret` 是本服务与 MC 服务器预共享的密钥。`serverId` 由 authlib-injector 在 `/mc/join` 阶段生成。

```
MC 服务端                YggAuth
  │  POST /mc/join         │
  │  {serverId, ...}       │
  │───────────────────────>│  校验令牌有效
  │                        │  计算 uuid = sha1(serverId + secret + uuid) 的十六进制
  │                        │  存入 server_session
  │  <────────────────────│
  │                       │
  │  进服时                │
  │  GET /mc/hasJoined      │
  │  ?username=X&serverId=Y │
  │───────────────────────>│  重算 hash,与会话中记录比对
  │  <────────────────────│
```

**必须正确的五点**:
1. `sharedSecret` 与 MC 服务端配置**完全一致**
2. `uuid` 格式正确(无横线小写 32 位 hex)
3. 哈希拼接顺序:`serverId + sharedSecret + uuid`(不是 uuid 在前)
4. 输出的 `id` 字段不带横线
5. `hasJoined` 必须在 `/mc/join` 之后、配额窗口内才通过

任一处出错,MC 服务端会静默拒绝进服,表现为「认证成功但连不上游戏」。

**防重放**:`server_session` 记录 `serverId` 与时间窗,同一 serverId 短时间只允许一次 `hasJoined` 成功。

**离线服务器处理**:无 `serverId` 或未登记的服务器 → 返回 **204 No Content**,MC 服务端据此拒绝进服。

### 2.3 其他端点

| 端点 | 要点 |
|---|---|
| `/mc/refresh` | 用 accessToken 换新 accessToken(clientToken 保持) |
| `/mc/validate` | 返回 204 有效 / 403 无效,无响应体 |
| `/mc/invalidate` | 吊销令牌,返回 204 |
| `/mc/signout` | 用户名+密码,吊销该用户全部令牌 |
| `/mc/` | metadata,必须返回 `skinDomains` |

**metadata 响应**
```json
{
  "meta": { "serverName": "YggAuth", "implementationName": "yggauth", "implementationVersion": "1.0" },
  "skinDomains": ["https://auth.example.com"],
  "signaturePublickey": "-----BEGIN PUBLIC KEY-----..."
}
```

`skinDomains` 决定 MC 客户端从哪个域加载皮肤,**必须是本服务的 `PUBLIC_BASE_URL`**。

## 三、档案与改名

### 3.1 UUID 派生

```
profile_uuid = uuidv5(NAMESPACE_YGG_AUTH, account_id)
```

- 命名空间常量固定,写进代码并加注释说明**永不可改**(改了所有玩家 UUID 都会变)
- 确定性派生:同一账号永远同一 UUID,无需存储即可复现
- `account_id` 作为输入使 UUID 不可反推

### 3.2 改名

```
PATCH /api/account/mc/name
{ "new_name": "NewName_1" }
```

**校验**:
- 格式 `^[A-Za-z0-9_]{3,16}$`
- 不在 `name_history` 中(旧名保留期内禁止重注册)
- 不被其他 profile 占用

**改名后**:
- `profile.current_name` 更新
- 旧名写入 `name_history`
- 材质绑定不变(按 profile_id 而非名字)

> **待定**:旧名保留多久、是否允许抢注腾出的名字。见 [04-decisions.md](./04-decisions.md) 第三节。

## 四、皮肤站

### 4.1 存储布局

```
data/textures/ab/abcdef1234...png      # 按 sha256 前 2 位分目录,避免单目录文件过多
data/avatars/<profile_uuid>.png         # 渲染后的头像
```

**接口**
```go
type Storage interface {
    Put(ctx context.Context, key string, data []byte) error
    Get(ctx context.Context, key string) ([]byte, error)
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

本期只实现本地磁盘(ADR-007),接口预留 S3 实现。

### 4.2 上传校验

| 检查项 | 规则 |
|---|---|
| 魔数 | 前 8 字节必须是 PNG 签名 |
| 皮肤尺寸 | 64×64(旧版)/ 64×32(旧版)/ 64×64(现代) |
| 披风尺寸 | 64×32 |
| 文件大小 | ≤ 2MB |
| 内容 | 实际解码校验,不只看文件头 |

> **注意**:MC 1.8+ 的皮肤是 64×64,1.7 及更早是 64×32。两者都要支持。

### 4.3 去重机制

```
上传 PNG → 计算 sha256 → 查 mc_texture 表
  ├── 命中:ref_count++ ,profile_texture 指向已有记录,直接返回(不重复落盘)
  └── 未命中:落盘 + 插入记录(ref_count=1)
```

**回收**:`ref_count` 归零且超过 7 天未访问的文件由后台任务清理。

### 4.4 头像渲染

从皮肤 PNG 提取头部区域,缩放为正方形头像:

```
64×64 皮肤布局:头部在 (8,8)-(16,16),第二层在 (40,8)-(48,16)
  → 裁出 8×8,叠加上层 → 放大到目标尺寸
64×32 皮肤布局:头部在 (8,8)-(16,16),第二层在 (16,8)-(24,16)
```

**异步渲染**:
- 请求 `/mc/avatar/:id` 时,若缓存命中直接返回
- 未命中则**提交到有界队列**并立即返回默认头像
- worker 渲染完成后写入 `minecraft.avatar` 表
- 队列满 → 返回默认头像,不阻塞请求
- **主请求处理路径绝不执行像素运算**

**缓存失效**:`minecraft.avatar.hash` 记录渲染时的皮肤 hash,皮肤变更后 hash 不匹配即重新渲染。

**默认头像**:按 UUID 哈希生成确定性颜色底 + 首字母,无外部依赖。

### 4.5 外部皮肤站回源

`MC_SKIN_EXTERNAL=true` 时,本地无材质且账号绑定了外部皮肤站账号,则回源拉取:

```
本地缓存未命中
  → 查 profile 的 external_binding
  → 调外部站 API(5s 超时)
      ├── 成功:缓存到本地 + 返回
      └── 失败:降级默认皮肤(不报错,不阻塞)
```

**熔断**:连续失败达阈值后短路一段时间,期间直接降级,不发起请求。避免外部站故障拖垮本服务。

## 五、隔离性要求

MC 域必须与 OIDC 域**完全隔离**,这是 M4/M5 的验收重点:

| 维度 | 隔离方式 |
|---|---|
| 令牌 | `minecraft.access_token` 与 `oidc.access_token` 是不同的表 |
| 签名密钥 | `minecraft.signing_key`(kid 前缀 `mc-`)与 `oidc.signing_key`(`oidc-`)分离 |
| issuer | 完全不同的字符串 |
| 故障 | MC 域 DB 不可用时,OIDC 与后台必须照常 200 |

**故障注入测试**(`isolation.spec`):
1. MC 限流打满 → OIDC P99 无变化
2. MC 写锁占用 → OIDC 正常签发令牌
3. 外部皮肤站宕机 → OIDC 全链路无异常
4. 材质存储不可读 → OIDC 令牌签发正常
5. MC 域 5xx → `/oauth/*` 与 `/api/admin/*` 全部 200

## 六、测试要点

**无真实 MC 环境时的替代验证**(已知局限,如实记录):

| 测试 | 方法 |
|---|---|
| 签名正确性 | 用 MC 端同样的算法重算,比对结果 |
| 防重放 | 同 serverId 二次 hasJoined 应失败 |
| 离线服务器 | 传随机 serverId,应 204 |
| 五生效点 | 逐个参数变更,确认恰好一个点失败 |
| 令牌隔离 | MC 令牌不能用于 `/oauth/userinfo`,反之亦然 |

> **交付时必须如实说明**:真实 MC 服务端 + authlib-injector 进服需要外部 Java 环境,若未实测则明确标注「协议级验证通过,端到端未实测」。

---

**上一篇**:[05-api.md](./05-api.md) —— API 设计
**下一篇**:[07-frontend.md](./07-frontend.md) —— 前端设计
