package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Validate 的四种拒绝情形:未知键、类型错、越界、枚举取值非法。
// 它们以前全都能写进库里 —— 每一种都对应一条「改不动的配置」。
func TestValidateRejectsUnknownAndInvalid(t *testing.T) {
	_, err := Validate("nope.key", json.RawMessage("1"))
	require.Error(t, err, "未登记的键必须拒绝")

	_, err = Validate("password.min_length", json.RawMessage(`"abc"`))
	require.Error(t, err, "整数键收到字符串必须拒绝")

	_, err = Validate("password.min_length", json.RawMessage("3"))
	require.Error(t, err, "低于下界必须拒绝")

	_, err = Validate("password.min_length", json.RawMessage("9999"))
	require.Error(t, err, "高于上界必须拒绝")

	_, err = Validate("password.min_length", json.RawMessage("8.5"))
	require.Error(t, err, "小数必须拒绝:JSON 没有整数/浮点之分,靠值判")

	_, err = Validate("registration.mode", json.RawMessage(`"semi_open"`))
	require.Error(t, err, "枚举取值必须拒绝")

	_, err = Validate("password.reject_common", json.RawMessage(`"yes"`))
	require.Error(t, err, "布尔键收到字符串必须拒绝")

	_, err = Validate("password.reject_common", json.RawMessage(`true`))
	require.NoError(t, err)

	spec, err := Validate("password.min_length", json.RawMessage("12"))
	require.NoError(t, err)
	require.Equal(t, 8, spec.Min)
	require.Equal(t, 128, spec.Max)
}

// 跨键约束:两个键各自合法,合起来却可能让注册链路全灭。
func TestCheckPasswordBounds(t *testing.T) {
	require.NoError(t, CheckPasswordBounds(8, 128))
	require.Error(t, CheckPasswordBounds(200, 128))
}

// 审计里的敏感值必须是掩码 —— 审计表会被读、被导出、被后台展示。
func TestAuditValueMasksSecrets(t *testing.T) {
	require.Equal(t, MaskValue, AuditValue("mail.password", json.RawMessage(`"hunter2"`)))
	require.Equal(t, `"open"`, AuditValue("registration.mode", json.RawMessage(`"open"`)))
	require.Equal(t, "12", AuditValue("password.min_length", json.RawMessage("12")))
}

// Schema 必须覆盖全部登记项,且枚举连标签一起下发。
//
// 标签要是在前端再抄一份,"invite_only" 就会以英文原始值出现在下拉框里,
// 或者某天后端改了取值而前端标签原地不动。
func TestSchemaCoversRegistryWithLabels(t *testing.T) {
	schema := Schema()
	require.Len(t, schema, len(registry))

	var mode map[string]any
	for _, row := range schema {
		require.NotEmpty(t, row["key"])
		require.NotEmpty(t, row["type"])
		if row["key"] == "registration.mode" {
			mode = row
		}
	}
	require.NotNil(t, mode, "registration.mode 必须在 schema 里")

	enum, ok := mode["enum"].([]map[string]string)
	require.True(t, ok, "枚举要以 {value,label} 下发")
	require.Equal(t, []map[string]string{
		{"value": "open", "label": "开放注册"},
		{"value": "invite_only", "label": "需要邀请码"},
		{"value": "closed", "label": "关闭注册"},
	}, enum)
}

// 登记表必须与 00004 的种子行一致:迁移里有、登记表没有的键 =
// 后台改不了的死键;反过来则是「后台能建但没人读」的孤儿键。
func TestRegistryMatchesMigrationSeed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "00004_init_app.sql"))
	require.NoError(t, err)

	const marker = "INSERT INTO app.setting"
	idx := strings.Index(string(raw), marker)
	require.GreaterOrEqual(t, idx, 0, "迁移里应有 app.setting 的种子 INSERT")
	insert := string(raw)[idx:]
	if end := strings.Index(insert, ";"); end > 0 {
		insert = insert[:end]
	}

	re := regexp.MustCompile(`'([a-z_]+\.[a-z_]+)'`)
	migrated := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(insert, -1) {
		migrated[m[1]] = true
	}
	require.NotEmpty(t, migrated, "应解析出迁移里的种子键")

	for key := range migrated {
		_, ok := Lookup(key)
		require.True(t, ok, "迁移种了 %s,但登记表里没有它 —— 后台将无法修改这个键", key)
	}
}

// 没有装配存储时,读取必须安静地退回调用方给的默认值 ——
// 否则一个「配置没接上」的部署会读到零值,把最短密码长度变成 0。
func TestSnapshotFallsBackWithoutStore(t *testing.T) {
	s := NewSnapshot(SettingsOptions{})
	require.Equal(t, 8, s.Int(t.Context(), "password.min_length", 8))
	require.Equal(t, "open", s.String(t.Context(), "registration.mode", "open"))
	require.Equal(t, true, s.Bool(t.Context(), "password.reject_common", true))
}
