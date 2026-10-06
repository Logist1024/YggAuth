/**
 * 审计日志里会出现的「动作」全集,给两处审计页的筛选框与「操作」列做翻译。
 *
 * 来源是**后端写审计的调用点**(grep `auditAdmin(` 与 `Action:` 字段逐个数出来的),
 * 不是拍脑袋列的:漏一个,用户在下拉里就永远想不到还有这类事件,
 * 表格里也会退回成 `account.login` 这种英文原串。
 *
 * 三个边界,都是「为什么它只是建议、不是下拉单选」的理由:
 *
 * 1. **它不是白名单**。筛选框仍是自由文本的等价物 —— 后端将来新增动作照样能查,
 *    只是不出现在建议里。后端 SQL 是 `action LIKE`(不写通配符时等价于精确匹配),
 *    所以「敲错一个字母就是 0 条、而空态不告诉你原因」才是要给建议的真正理由。
 * 2. label 写成「中文 · 原始串」而不是只写中文:下拉按 label 过滤,
 *    于是输「登录」或输 `password` 都能筛出同一条;真提交的仍是 value(原始串)。
 * 3. 中文译名只做展示,查询、URL 里流转的始终是 value —— 表格与链接不含本地化内容。
 */
export interface AuditAction {
  /** 后端写进审计表的原始动作串,直接作为查询参数。 */
  value: string
  /** 中文含义,表格展示与下拉里的人话部分。 */
  title: string
}

/**
 * 按「账号 → 邀请 → 权限 → 配置」分组排序。
 * 分组只为查找顺手,与后端写入顺序无关。
 */
export const AUDIT_ACTIONS: readonly AuditAction[] = [
  { value: 'account.register', title: '注册' },
  { value: 'account.login', title: '登录' },
  { value: 'account.verify_email', title: '验证邮箱' },
  { value: 'account.email_verification_resent', title: '重发验证邮件' },
  { value: 'account.email_changed', title: '更换邮箱' },
  { value: 'account.profile_updated', title: '修改资料' },
  { value: 'account.password_change', title: '修改密码' },
  { value: 'account.password_reset_requested', title: '申请重置密码' },
  { value: 'account.password_reset', title: '邮件重置密码' },
  { value: 'account.status_changed', title: '停用 / 启用账号' },
  { value: 'invitation.created', title: '生成邀请码' },
  { value: 'invitation.revoked', title: '撤销邀请码' },
  { value: 'rbac.role_created', title: '创建角色' },
  { value: 'rbac.role_updated', title: '修改角色' },
  { value: 'rbac.role_deleted', title: '删除角色' },
  { value: 'rbac.role_granted', title: '授予角色' },
  { value: 'rbac.role_revoked', title: '收回角色' },
  { value: 'setting.updated', title: '修改配置' },
]

/** 下拉建议的成品:{ value: 原始串, label: 中文 · 原始串 }。 */
export const AUDIT_ACTION_OPTIONS: readonly { value: string; label: string }[] = AUDIT_ACTIONS.map((a) => ({
  value: a.value,
  label: `${a.title} · ${a.value}`,
}))

/**
 * 动作 → 中文,给表格「操作」列当翻译表;查不到的退回原始串。
 *
 * 此前账号页自己抄过一份(含已停写的 `account.logout*`、缺一半新动作),
 * 两个页面的同一张表各写各的迟早漂移,所以并到这里一处维护。
 */
export const ACTION_LABEL: Readonly<Record<string, string>> = Object.fromEntries(
  AUDIT_ACTIONS.map((a) => [a.value, a.title]),
)
