# 前端架构

> Vue 3 + TypeScript + Vite + Ant Design Vue 4。两个 SPA 共享 `web/shared` 包。

---

## 一、Workspace 结构

```
web/
├── package.json            # workspace 根
├── pnpm-workspace.yaml
├── eslint.config.mjs
├── tsconfig.base.json
├── apps/
│   ├── account/            # 终端用户 SPA
│   │   ├── src/
│   │   │   ├── App.vue
│   │   │   ├── main.ts
│   │   │   ├── router.ts
│   │   │   ├── stores/session.ts
│   │   │   ├── layouts/AppLayout.vue
│   │   │   ├── layouts/AuthLayout.vue
│   │   │   ├── views/*.vue
│   │   │   └── components/*.vue
│   │   └── vite.config.ts  # outDir: ../../../internal/webserver/dist/account
│   └── admin/              # 管理后台 SPA
│       ├── src/
│       │   ├── App.vue
│       │   ├── main.ts
│       │   ├── router.ts
│       │   ├── stores/admin.ts
│       │   ├── layouts/AdminLayout.vue
│       │   ├── views/*.vue
│       │   └── components/*.vue
│       └── vite.config.ts  # outDir: ../../../internal/webserver/dist/admin
└── shared/                 # 共享包
    ├── src/
    │   ├── index.ts        # 统一导出
    │   ├── api.ts          # 统一响应处理
    │   ├── codes.ts        # 错误码表(与后端 apperr 同步)
    │   ├── site.ts         # 站点公开配置类型
    │   ├── permissions.ts  # 权限点定义
    │   ├── validation.ts   # 表单校验规则
    │   ├── ui.ts           # UI 工具
    │   ├── format.ts       # 格式化
    │   └── audit.ts        # 审计日志类型
    └── package.json
```

---

## 二、构建产物

- Vite `outDir` 指向 `internal/webserver/dist/{account,admin}`
- `go:embed all:dist` 把产物编进二进制(见 `internal/webserver/webserver.go`)
- **改前端必须重建**:`pnpm build` 重新生成 dist,否则 `go build` 嵌的是旧产物

---

## 三、品牌主题

两套主题(见 `web/shared/src/site.ts`):

| clientId | 标题 | 主色 | 场景 |
|---|---|---|---|
| `app` | YggAuth · 账号中心 | `#3b6ea5` | 业务系统登录 |
| `mc` | YggAuth · Minecraft 登录 | `#5a8f3c` | MC 服务器登录 |

通过查询参数 `?client_id=mc` 切换(见 `internal/webserver/dist/account/index.html`)。

---

## 四、路由与状态

### 4.1 账号站路由

```typescript
// web/apps/account/src/router.ts
const routes = [
  { path: '/', component: OverviewView },
  { path: '/login', component: LoginView },
  { path: '/register', component: RegisterView },
  { path: '/verify-email', component: VerifyEmailView },
  { path: '/forgot-password', component: ForgotPasswordView },
  { path: '/reset-password', component: ResetPasswordView },
  { path: '/security', component: SecurityView },
  { path: '/sessions', component: SessionsView },
  { path: '/skin', component: SkinView },
  { path: '/audit', component: AuditView },
  { path: '/404', component: NotFoundView },
]
```

### 4.2 管理后台路由

```typescript
// web/apps/admin/src/router.ts
const routes = [
  { path: '/admin/login', component: LoginView },
  { path: '/admin', component: AdminLayout, children: [
    { path: '', redirect: '/admin/dashboard' },
    { path: 'dashboard', component: DashboardView },
    { path: 'accounts', component: AccountsView },
    { path: 'roles', component: RolesView },
    { path: 'clients', component: ClientsView },
    { path: 'keys', component: SigningKeysView },
    { path: 'textures', component: MCTexturesView },
    { path: 'profiles', component: MCProfilesView },
    { path: 'audit', component: AuditView },
    { path: 'settings', component: SettingsView },
    { path: '404', component: NotFoundView },
  ]}
]
```

### 4.3 Store

- `web/apps/account/src/stores/session.ts`:会话状态、401 处理、导航
- `web/apps/admin/src/stores/admin.ts`:权限点缓存、菜单、通知

**401 处理**:`onUnauthorized` 写一次性 `notice`,避免用户以为被踢下线时看不到原因。

---

## 五、列表页通则

见 `web/apps/admin/src/views/AccountsView.vue` 等列表页:

1. **搜索防抖**:每次敲键不立即发请求,等用户停顿 300ms
2. **页脚总数**:显示「共 N 条」
3. **筛选状态进地址栏**:刷新不丢筛选条件
4. **空态**:无数据时展示 `EmptyState` 组件,带引导操作

---

## 六、表单提交

见 `web/apps/admin/src/views/SettingsView.vue`:

1. **提交函数防重入**:`submitting` 标志,提交期间禁用按钮
2. **回车提交**:表单支持 Enter 触发提交
3. **统一错误处理**:`api.ts` 的 `request()` 自动解析信封、抛出业务错误

---

## 七、页面切换动画

- 进场:淡入 + 上移 6px / 160ms
- **只做进场不做退场**(退场要么变慢、要么新旧叠屏)
- `prefers-reduced-motion: reduce` 下完全不动

---

## 八、开发服务器

```bash
cd web && pnpm dev
```

启动两个独立 Vite 服务器:
- 账号站: **5173**
- 管理后台: **5174**

代理到后端 3000(见 `vite.config.ts`)。

**必须代理的路径**:
- `/api/*` → 3000
- `/oauth/*` → 3000
- `/mc/*` → 3000
- `/admin/*` → 3000

---

## 九、Lint 与 Typecheck

```bash
cd web && pnpm typecheck  # vue-tsc --noEmit
cd web && pnpm lint       # eslint
```

CI 跑 `make verify` 包含前端 lint。
