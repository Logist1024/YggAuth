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

| 路由 | 页面 | 说明 |
|---|---|---|
| `/login` | 登录 | 邮箱 + 密码 |
| `/register` | 注册 | 用户名 + 邮箱 + 密码 + 邀请码 |
| `/forgot-password` | 忘记密码 | 邮箱输入 |
| `/reset-password` | 重置密码 | 令牌 + 新密码 |
| `/verify-email` | 邮箱验证 | 令牌自动提交 |
| `/me` | 账号概览 | 基本信息、状态 |
| `/me/profile` | 资料编辑 | 改用户名/邮箱 |
| `/me/security` | 安全设置 | 改密码、会话列表 |
| `/me/sessions` | 会话管理 | 活跃会话、踢下线 |
| `/me/mc` | MC 档案 | 绑定、改名、MC 登录开关 |
| `/me/mc/skin` | 皮肤管理 | 上传/预览/删除皮肤与披风 |

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
后端 Go 定义规则常量
  → 暴露 GET /api/config/validation
  → 前端启动时拉取,构建 Ant Design 表单 rules
```

密码规则:8–128 位,不强制组合,可选强度提示(弱/中/强)。

## 四、admin-web 页面

| 路由 | 页面 | 所需权限点 |
|---|---|---|
| `/dashboard` | 仪表盘 | 登录即可 |
| `/accounts` | 账号管理 | `account:read` |
| `/roles` | 角色权限 | `rbac:read` |
| `/invitations` | 邀请管理 | `rbac:write` |
| `/audit` | 审计日志 | `audit:read` |
| `/audit/settings` | 审计配置 | `audit:export` |
| `/clients` | OIDC 客户端 | `oidc:client:read` |
| `/mc/profiles` | 玩家档案 | `minecraft:profile:read` |
| `/mc/textures` | 材质库 | `minecraft:texture:read` |
| `/settings` | 应用配置 | `setting:read` |

### 权限驱动

**菜单**:`GET /api/admin/menus` 返回按当前管理员权限点过滤后的菜单树,前端直接渲染。

**路由守卫**:`RequirePermission` 组件校验权限点,无权限跳 403 页。**但真正的拦截在后端** —— 前端守卫只是体验优化,安全必须由服务端保证。

## 五、API 层

### 5.1 统一请求封装

```ts
// 响应包解包
const res = await request<{code:number; message:string; data:T}>(url, init)
if (res.code !== 0) throw new ApiError(res.code, res.message)
return res.data
```

### 5.2 认证处理

- 请求带 `credentials: 'include'` 携带 Cookie
- 收到 401 → 尝试 `POST /api/auth/refresh` 刷新会话
- 刷新失败 → 跳转登录页并带 `redirect` 参数
- **并发 401 去抖**:多个请求同时 401 只触发一次刷新

### 5.3 错误提示

按 `code` 映射到中文提示,集中在 `errors.ts` 一处管理:

```ts
export const ERROR_MESSAGES: Record<number, string> = {
  10001: '请求参数有误',
  20002: '邮箱或密码错误',
  20006: '尝试次数过多,请 15 分钟后再试',
  // ...
}
```

## 六、状态管理

Pinia store 划分:

| Store | 内容 |
|---|---|
| `useAuthStore` | 当前登录账号、会话、权限点 |
| `useConfigStore` | 校验规则、应用配置 |
| `useAdminStore` | 菜单、角色、权限点目录 |

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
