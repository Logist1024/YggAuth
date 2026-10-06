-- M1 基线迁移:app schema
--
-- 运行时可改的应用配置。优先级高于环境变量,便于后台调整而无需重启
-- (见 docs/deployment.md 5.3)。

-- +goose Up
-- +goose StatementBegin

CREATE SCHEMA IF NOT EXISTS app;

CREATE TABLE app.setting (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by UUID REFERENCES identity.account(id) ON DELETE SET NULL
);

CREATE TRIGGER trg_setting_updated_at
    BEFORE UPDATE ON app.setting
    FOR EACH ROW EXECUTE FUNCTION ygg_touch_updated_at();

-- 默认值与环境变量保持一致,启动时写入,之后由后台接管。
-- 值用 JSONB 存,前端拿到即结构化数据,不必再解析字符串。
INSERT INTO app.setting (key, value) VALUES
    ('registration.mode',        '"open"'::jsonb),
    ('registration.mc_login_default', 'true'::jsonb),
    ('password.min_length',      '8'::jsonb),
    ('password.max_length',      '128'::jsonb),
    ('password.reject_common',   'true'::jsonb),
    ('session.idle_ttl_hours',   '168'::jsonb),
    ('session.max_ttl_hours',    '720'::jsonb),
    ('login.max_failed_attempts','5'::jsonb),
    ('login.lock_seconds',       '900'::jsonb),
    ('mail.verify_cooldown_seconds', '60'::jsonb),
    ('mail.verify_daily_limit',  '5'::jsonb),
    ('mc.name_retention_days',   '90'::jsonb);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS app CASCADE;
-- +goose StatementEnd
