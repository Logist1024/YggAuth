# 07 · 前端设计

## 一、技术栈

| 项 | 选型 |
|---|---|
| 框架 | Vue 3.5(Composition API + `<script setup>`) |
| 语言 | TypeScript(strict) |
| 构建 | Vite 6 |
| 路由 | Vue Router 4 |
| 状态 | Pinia |
| UI | **Ant Design Vue 4** |
| 请求 | 原生 `fetch` 封装(不引入 axios) |
| 样式 | 原生 CSS + CSS 变量(不引入 UI 库样式体系) |

**两个独立应用**:

| 应用 | 目录 | 面向 |
|---|---|---|
| account-web | `web/account/` | 终端用户 |
| admin-web | `web/admin/` | 管理员 |

## 二、构建与集成

```
pnpm build          # 构建两个应用
        ↓
web/dist/account/   web/dist/admin/
        ↓ go:embed (internal/transport/static.go)
Go 二进制单文件,启动即可服务前端
```

**Makefile 集成**
```bash
make web        # pnpm build → 产物到 web/dist/
make build      # web + go build,含 embed
```

**SPA 路由回退**:Go 侧对未匹配的 GET 请求返回对应应用的 `index.html`,让前端路由接管。API 路径前缀(`/api`、`/oauth`、`/mc`、`/health`)不参与回退。

```go
mux.HandleFunc("/", func(w, r) {
    if isAPIPath(r.URL.Path) { http.NotFound(w, r); return }
    serveIndex(w, r)   // 按域名决定 account 还是 admin
})
```

**按域名分流**:`auth.example.com` → account-web,`admin.example.com` → admin-web。单域名部署时用路径前缀分流。

## 三、account-web 页面

布局外壳是登录态的 `/` 一组,顶部导航按这个表渲染;未登录访问任何受保护路由会跳 `/login?redirect=`。

| 路由 | 页面 | 说明 |
|---|---|---|
| `/login` | 登录 | 邮箱 + 密码 |
| `/register` | 注册账号 | 用户名 + 邮箱 + 密码;`registration.mode = invite_only` 时才显示邀请码 |
| `/forgot-password` | 找回密码 | 邮箱输入 |
| `/reset-password` | 重置密码 | 令牌 + 新密码 |
| `/verify-email` | 验证邮箱 | 令牌自动提交 |
| `/` | 账号概览 | 基本信息、MC 档案状态、最近登录 |
| `/security` | 安全设置 | 改密码、改资料 |
| `/skin` | 皮肤管理 | 上传/预览/删除皮肤与披风 |
| `/sessions` | 登录设备 | 活跃会话、单条踢下线、退出全部 |
| `/audit` | 操作记录 | 本人登录/安全事件,按结果与动作筛选 |
| `/:pathMatch(.*)*` | 页面不存在 | 404 |

**邮箱未验证**不是独立页面:登录后由外壳在顶栏挂一条黄色提示条,带「重发验证邮件」按钮
(`POST /api/auth/email/resend`),因为藏进「安全设置」里没有用户会去找。

### 品牌主题

按 `?client_id=` 切换主色与标题,共用同一套表单与校验:

| client_id | 名称 | 主色 |
|---|---|---|
| `app` | YggAuth 账号中心 | `#1677ff` |
| `mc` | YggAuth 游戏账号 | `#52c41a` |

用 Ant Design Vue 的 `ConfigProvider` 注入 token 实现,不做两套代码。

### 表单校验

**与后端共用同一份规则定义**,避免前后端不一致。做法:

```
后端 Go 定义策略常量
  → 暴露 GET /api/auth/policy
  → 前端路由守卫在首跳前拉取,存入 session store,构建 rules
```

`registration.mode` 也是从这里下发的 —— 注册页**运行时**决定要不要显示邀请码字段,
前端不硬编码「是否需要邀请码」。前端的 `validatePassword` 等函数复用 `@yggauth/shared`,
与后端同一套规则,只是响应时机不同(前端即时,后端最终裁决)。

密码规则:8–128 位,不强制组合,可选强度提示(弱/中/强)。

### 表单提交

**统一「回车即提交」**:主操作按钮一律 `html-type="submit"`,表单一律
`@submit.prevent="提交函数"`,不要给这个按钮再挂 `@click` —— 挂了会 click 与 submit 各触发一次。

**提交函数首行防重入**:`if (状态.value) return`。按钮的 `:loading` 只挡**点击**
(ant-design-vue 的 `handleClick` 在 loading 时直接返回),回车走的是表单的 submit、绕过按钮 ——
连发回车会重复登录(给限流计数多记一笔)、重复发一封重置邮件、同一条配置 PATCH 两次。
一行一个表单的页面按行记状态(配置页的 `savingKey`),不是一个全局布尔。

弹窗是唯一的例外:`a-modal` 的「确定」在 footer 上、并不在 `<a-form>` 内部,按钮提交不了这个表单。
这类改用 `@keydown.enter.exact.prevent="提交函数(fn, $event)"`,注意 **`$event` 必须显式传**:

> 带修饰符的处理器会被编译成 `$event => submitOnEnter(create)` —— 只是造出一个闭包再丢掉,
> 函数体永远不执行;而 `defaultPrevented` 仍是 `true`(`.prevent` 是 `withModifiers` 自己做的),
> 极具迷惑性。textarea 的回车留给换行,提交进行中不重复触发。

现状覆盖:登录/注册/找回/重置、安全设置的两块表单,后台的修改邮箱、生成邀请、
配置项的 12 行保存,以及客户端与角色的新建/编辑弹窗。

## 四、admin-web 页面

| 路由 | 页面 | 所需权限点 |
|---|---|---|
| `/dashboard` | 仪表盘 | 登录即可 |
| `/accounts` | 账号管理 | `account:read` |
| `/roles` | 角色权限 | `rbac:read` |
| `/invitations` | 邀请管理 | `rbac:write` |
| `/audit` | 审计日志 | `audit:read` |
| `/clients` | OIDC 客户端 | `oidc:client:read` |
| `/keys` | 签名密钥 | `oidc:client:read` |
| `/mc/profiles` | 玩家档案 | `minecraft:profile:read` |
| `/mc/textures` | 材质库 | `minecraft:texture:read` |
| `/settings` | 应用配置 | `setting:read` |
| `/:pathMatch(.*)*` | 404 | 登录即可 |

> **玩家档案 / 材质库**是普通列表页,各自请求 `/api/admin/mc/profiles`、`/api/admin/mc/textures`,
> 搜索、类型筛选、分页都与「账号管理」一致。
> 管理端**只读**:改名/封禁与材质删除的接口尚未实现(见 [05-api.md](./05-api.md) 第五节),
> 页面上没有这两个按钮是现状,不是漏做。

**列表页通则**(账号管理、审计日志、玩家档案、材质库,以及账号中心的「操作记录」):

- **页脚显示总数**:「共 N 条,当前 x–y」。总数本来就是各列表接口回的 `total`,
  之前页脚只有页码,用户无从得知一共有多少条、自己在看哪一段;
  `total=0` 时不显示这一行 —— 那一页已有空状态文案,再补一句「共 0 条」是噪音。
- **列表状态进地址栏**:搜索词、筛选条件、页码与每页条数都写进 query
  (`router.replace`),刷新不丢,链接可以直接发给同事,对方打开就是同一屏。
  用 `replace` 而不是 `push`:翻十页不该在浏览器历史里堆十条记录,
  后退键仍然该是「离开这一页」。只写与默认值不同的项,链接才短。
  地址栏里的值同样要过一遍校验(`readPage`/`readPageSize`/`readText`),
  脏链接退回默认值,而不是让 `?size=99999` 变成一屏渲染不动的表格。
- **搜索词、筛选词两端空格吃掉**:提交前 `trim()`,从地址栏读进来时同样 `trim()` ——
  「tester 」查不到任何人、「account.login 」对不上任何动作,而用户多半只是多按了一下空格。
- **闭集的筛选项做成下拉(带搜索),别当自由文本**:审计的「动作」是后端写死的 18 种,
  而 SQL 按 `action LIKE` 精确匹配 —— 敲错一个字母就是 0 条,空态只说
  「没有符合筛选条件的记录」,不告诉你是自己拼错了。下拉按「中文 · 原始串」过滤,
  输「登录」或输 `password` 都能搜到同一条,选完即查,与「结果」下拉同一种交互。
  清单放在 `web/shared/src/audit.ts`(照后端写审计的调用点数出来的),**不是白名单**:
  后端新增动作时补一条,老动作的原始串始终以表格里的值为准。
- **`a-select` 的清空按钮会把值清成 `undefined`**:遍历筛选字段做 `trim()` 时会当场抛错,
  表现是「点了 × 什么都没发生、地址栏参数原样留着」——取值处统一兜成 `''`。
  冒烟有反向断言(`SMOKE_UNEXPECT_QUERY`)盯这一条。
- **搜索框同样给一个 ×**(`allow-clear`):搜索词通常是敲错了再改,没有 × 就只能全选删;
  清空**只清输入框、不自动查询** —— 文字筛选的口径仍是回车/点按钮,
  与上面「选完即查」的下拉并不冲突:下拉选一项是一次完整表达,敲字不是。
  两处搜索框(账号管理、玩家档案)现在同一种交互。
- **危险操作的确认要写后果,不写「确定吗」**:停用/删除/轮换/撤销这类一行一确认的弹层,
  标题写清「会发生什么」——例如停用账号要说「现有登录态也会立刻失效」,
  依据是中间件**每个请求**都重查一次账号状态(`internal/transport/auth.go` 的 `SessionAuth`),
  不是照直觉写的;没验证过的后果宁可不写。

> **审计日志页**的两处「查得回来」的补齐:`目标 ID` 输入(后端 `auditFilter` 一直支持
> `target_id`,前端此前没给入口 —— 「目标」列明明显示着 `类型:ID` 却没地方贴);
> 账号详情里的**账号 ID 可复制**(`CopyableText`),复制完填进
> `?account_id=…` 就是「这个人做过什么」的那一屏。

**表格宽度**:内容区 `.page` 上限 1200px,宽表统一给 `:scroll="{ x: 1200 }"` ——
列总宽超过容器时出横向滚动,而不是把列压到读不了。
**窄屏**:`Sider` 的 `breakpoint="lg"` 配合 `collapsed-width="0"`,自动让开空间;
顶栏的「菜单」按钮(仅窄屏显示)手动切换,避免自动收起后无路可开。

### 权限驱动

**菜单**:`GET /api/admin/menus` 返回按当前管理员权限点过滤后的菜单树,前端直接渲染。

**路由守卫**:全局 `beforeEach` 按路由的 `meta.permission` 拦下**直接输 URL** 的访问
(只藏菜单不够:菜单是给人看的,URL 是给机器用的,而管理员既是人也是机器)。
无权限时跳回仪表盘,并把原因写进 `store.notice` —— 由 `AdminLayout` 在内容区顶部
渲染一条可关闭的提示;静默跳转在用户眼里就是「点了菜单没反应」,于是他会再点一次、再点一次。
**但真正的拦截在后端**(`httpx.RequirePermission`)—— 前端守卫只是体验优化,安全必须由服务端保证。

## 五、API 层

### 5.1 统一请求封装

```ts
// shared/src/api.ts:拆信封、抛 ApiError、401 触发一次刷新、刷新失败跳登录
const data = await store.api.post<T>('/api/auth/login', body)
```

`ApiClient` 与 `ApiError` 全部放在 `@yggauth/shared`,两个 SPA 共用一份 ——
逻辑一旦分进两个应用,行为迟早会不一致(表现为一边能自动续期一边不能)。

### 5.2 认证处理

- 请求带 `credentials: 'include'` 携带 Cookie
- 收到 401 → 尝试 `POST /api/auth/refresh` 刷新会话
- 刷新失败 → 跳转登录页并带 `redirect` 参数
- **并发 401 去抖**:多个请求同时 401 只触发一次刷新

### 5.3 错误提示

按 `code` 映射文案,集中在 `shared/src/codes.ts` 一处管理,导出 `codeTitle(code)` 与
`codeHint(code)`。`ApiError` 构造时就把两者算进 `title` / `hint` 字段,页面不必再查表。

**统一错误横幅**:所有失败一律经 `errorBanner(err, 兜底文案)` 转成结构化提示:

```ts
interface ErrorBanner {
  title: string  // 发生了什么
  hint: string   // 用户可以怎么做(后端 codeHint)
  trace: string  // 错误码 + HTTP 状态;网络失败时为空
}
```

渲染端只有一个组件 `ErrorAlert.vue`(两 SPA 各一份,同 `EmptyState.vue` 的双份模式),
三段式分行展示。**错误码单独成行**,报障时用户能直接念出后端日志的检索线索。

三条分类规则:

| `err` 类型 | 结果 |
|---|---|
| `ApiError` | 后端 message + codeHint + `错误码 N · HTTP M` |
| `TypeError`(断网/DNS/CORS) | 「无法连接服务器 · 请检查网络连接后重试」,无错误码(硬凑会误导排查) |
| 其它异常 | 调用方给的兜底文案 |

本地校验(没有异常可抛)用 `bannerMessage(文案)`,返回同一种结构 ——
否则同一页上「后端报错」和「本地校验失败」会长得不一样。

> 不用 `message.success()` 之类的全局静态提示:没有 `App.useApp()` 上下文时
> Ant Design Vue 会发主题警告,且无法跟随页面布局定位。成功反馈一律用
> 就地的 `a-alert type="success"`(改完不关抽屉,让结果可见)。

## 六、状态管理

Pinia store 划分:

| Store | 归属 | 内容 |
|---|---|---|
| `session` | account-web | 当前账号、登录策略、主题、`ApiClient` |
| `admin` | admin-web | 菜单、管理员信息、`ApiClient` |

角色/权限点目录不进 store,用到时直接拉接口 —— 数据由后端按权限过滤,
前端缓存反而容易在授权变更后显得过期。

**原则**:服务端状态用 store 缓存,**不引入额外的数据请求库**。需要在组件间共享的才进 store,其余用局部 `ref`。

## 七、皮肤管理界面

- 上传前**客户端预校验**:尺寸、大小、类型
- 预览:用 canvas 绘制 2D 皮肤模型(正面/背面/头部放大)
- 头像:直接用 `/mc/avatar/:id?size=128` 链接
- 大文件上传显示进度(用 `fetch` + `ReadableStream`,或 `XMLHttpRequest`)

## 八、代码规范

| 项 | 规则 |
|---|---|
| ESLint | `eslint-plugin-vue` + `typescript-eslint`,strict |
| Prettier | 与 Go 无关,前端独立配置 |
| 组件命名 | `PascalCase.vue` |
| Composition API | 一律 `<script setup>`,不用 Options API |
| 类型 | `strict: true`,禁止 `any`(确需时显式注释) |
| 导入顺序 | ESLint `import/order` 强制 |

## 九、前端安全

| 风险 | 措施 |
|---|---|
| XSS | Vue 默认转义;`v-html` 禁用,确需时用 DOMPurify |
| CSRF | SameSite=Lax Cookie + 写操作校验 Origin |
| 令牌泄漏 | 不存 localStorage,全用 HttpOnly Cookie |
| 敏感信息 | 密码字段不回显,日志不打印 |
| 点击劫持 | `X-Frame-Options: DENY` |

## 十、构建产物

| 应用 | 产物 | 压缩后目标 |
|---|---|---|
| account-web | `web/dist/account/` | < 400KB(gzip) |
| admin-web | `web/dist/admin/` | < 500KB(gzip) |

超过目标时用路由级代码分割 + Ant Design 组件按需引入。

---

**上一篇**:[06-mc-protocol.md](./06-mc-protocol.md) —— Minecraft 协议
**下一篇**:[08-deployment.md](./08-deployment.md) —— 容器化部署
