-- P1 迁移:首登强制改密的两面旗标
--
-- 设计取舍(见 docs/configuration.md 5.6):
--
--	credential.must_change          是「这枚凭据需要更换」的**真源**,
--	                                由引导流程置位、由改密/重置清零。
--	session.must_change_password    是它的**投影像**,在登录成功那一刻
--	                                从凭据抄到会话上。
--
-- 为什么要有两份:每个请求都要判断「是否处于强制改密状态」,
-- 而认证中间件手里只有会话行。若把判断放在凭据行上,每个请求都要
-- 多打一次 identity.credential —— 为了一个极少数账号才有的旗标,
-- 让全部请求的热路径多一次查询不划算。会话行本来就是必读的,
-- 把旗标抄到会话上等于把成本固定为零。
--
-- 反向约束也在这里成立:改密码会吊销该账号全部会话
-- (handler 的 RevokeAll),所以投影像不需要单独清理 ——
-- 会话没了,旗标自然消失;而凭据上的真源由改密流程显式清零。

-- +goose Up
-- +goose StatementBegin

ALTER TABLE identity.credential
    ADD COLUMN IF NOT EXISTS must_change BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE identity.session
    ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE identity.session
    DROP COLUMN IF EXISTS must_change_password;

ALTER TABLE identity.credential
    DROP COLUMN IF EXISTS must_change;

-- +goose StatementEnd
