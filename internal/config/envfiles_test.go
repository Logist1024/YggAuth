package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/config"
)

// 这一组测试守的是 docs/configuration.md 的 D2/D3:
// 「读取清单、文档清单、compose 透传清单」三者必须互相对齐。
//
// 对不齐的后果不是报错,而是**静默失效** —— 运维改了 .env,进程读的还是
// 代码默认值,而且没有任何日志能提示。这类问题人工核对一定会漏,
// 所以写成测试:谁新增一个配置项而忘了同步另外两处,提交前就红。
//
// 放在 internal/config 而不是 internal/deploycheck,是因为它不需要
// embedded-postgres、不打 integration 标签,`make verify` 就能跑。

// repoRoot 从 internal/config/ 上溯两层到仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	require.NoError(t, err, "文件 %s 必须存在", rel)
	return string(raw)
}

// configKeyPattern 匹配 Load 里所有读取环境变量的调用。
//
// config.go 的读取点只有 `r.<method>("KEY", …)` 这一种写法(见 reader 的
// str/required/intVal/intBetween/boolVal/duration/seconds/csv/oneOf),
// 所以一个正则就能拿全。像 `r.oneOf("REGISTRATION_MODE", r.str("REGISTRATION_MODE", …))`
// 这种把同一个键读两遍的写法,集合去重后就是一个键。
var configKeyPattern = regexp.MustCompile(`\br\.[a-zA-Z]+\("([A-Z][A-Z0-9_]+)"`)

// configEnvKeys 返回 config.Load 会读取的全部环境变量名。
func configEnvKeys(t *testing.T) map[string]bool {
	t.Helper()

	keys := map[string]bool{}
	for _, m := range configKeyPattern.FindAllStringSubmatch(repoFile(t, "internal/config/config.go"), -1) {
		keys[m[1]] = true
	}
	require.NotEmpty(t, keys,
		"config.go 里没扫到任何配置读取点 —— 要么正则失效,要么读取写法变了,先改这个测试")
	return keys
}

// envExampleKeys 返回 .env.example 里列出的变量名(只认非注释的 KEY= 行)。
func envExampleKeys(t *testing.T) map[string]bool {
	t.Helper()

	keys := map[string]bool{}
	for _, line := range strings.Split(repoFile(t, ".env.example"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if key, _, ok := strings.Cut(trimmed, "="); ok {
			keys[strings.TrimSpace(key)] = true
		}
	}
	require.NotEmpty(t, keys)
	return keys
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// appServiceEnvKeys 取出 compose 里 app 服务 environment 段的键名。
//
// 只认 app 这一个服务:postgres 的 POSTGRES_* 是数据库自己的环境变量,
// 拿去和 config.Load 对齐只会得到一堆假的「死配置」。
func appServiceEnvKeys(t *testing.T) map[string]bool {
	t.Helper()

	lines := strings.Split(repoFile(t, "docker-compose.yml"), "\n")

	start := -1
	for i, line := range lines {
		if line == "  app:" {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "docker-compose.yml 里必须有 `app:` 服务")

	// 服务块:从 `  app:` 到下一个恰好两格缩进的服务名为止。
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line != "" && line[0] == ' ' && line[1] == ' ' &&
			(len(line) == 2 || line[2] != ' ') {
			end = i
			break
		}
	}

	keys := map[string]bool{}
	inEnvironment := false
	for _, line := range lines[start+1 : end] {
		if line == "    environment:" {
			inEnvironment = true
			continue
		}
		if inEnvironment {
			// environment 段结束的标志:出现与它同级(恰好四格缩进)的键。
			if line != "" && !strings.HasPrefix(line, "     ") {
				inEnvironment = false
				continue
			}
			if m := regexp.MustCompile(`^      ([A-Z][A-Z0-9_]+):`).FindStringSubmatch(line); m != nil {
				keys[m[1]] = true
			}
		}
	}
	require.NotEmpty(t, keys, "app 服务的 environment 段没扫到任何键")
	return keys
}

// TestConfigKeysAreAllDocumented 是正向要求:读的每个变量都必须在
// .env.example 里有一行。
//
// 缺一行的后果是「运维不知道有这个开关」;compose 改成 env_file 直透之后,
// 它更会成为一个无法从部署侧观察到的隐藏默认值。
func TestConfigKeysAreAllDocumented(t *testing.T) {
	config := configEnvKeys(t)
	documented := envExampleKeys(t)

	missing := map[string]bool{}
	for key := range config {
		if !documented[key] {
			missing[key] = true
		}
	}
	require.Empty(t, missing,
		"以下配置项被 config.Load 读取,但 .env.example 没有列出:%s",
		strings.Join(sortedKeys(missing), ", "))
}

// TestEnvExampleHasNoDeadKeys 是反向要求:文档里不许出现读不到的变量。
//
// 死键比缺键更害人 —— 运维照着改,改完什么都没发生,而文档还写着它有效。
func TestEnvExampleHasNoDeadKeys(t *testing.T) {
	config := configEnvKeys(t)
	documented := envExampleKeys(t)

	dead := map[string]bool{}
	for key := range documented {
		if !config[key] {
			dead[key] = true
		}
	}
	require.Empty(t, dead,
		".env.example 列出了 config.Load 根本不读的变量(改了不会生效):%s",
		strings.Join(sortedKeys(dead), ", "))
}

// TestComposeAppEnvKeysAreAllRead 是 D3 的回归测试。
//
// 曾经 compose 传的是 SMTP_USERNAME,而 config 读的是 SMTP_USER ——
// 一开 MAILER_TRANSPORT=smtp 就启动失败,而且两边都不报错,
// 因为「传了但没人读」这个状态本身没有任何运行时反馈。
// 本测试把「传给 app 的每个键都必须被读取」钉成契约。
func TestComposeAppEnvKeysAreAllRead(t *testing.T) {
	config := configEnvKeys(t)
	passed := appServiceEnvKeys(t)

	dead := map[string]bool{}
	for key := range passed {
		if !config[key] {
			dead[key] = true
		}
	}
	require.Empty(t, dead,
		"compose 给 app 传了 config.Load 不读的变量(死配置,改了不会生效):%s",
		strings.Join(sortedKeys(dead), ", "))
}

// TestComposeLoadsEnvFile 是 D2 的回归测试。
//
// 手工透传清单是漂移的根源:实测 80 个配置项里只有 17 个能被 .env 控制,
// 其余 63 个改了等于没改。这里钉住「app 必须整份吃 .env」,
// 谁再退回去逐条手抄,测试立刻失败。
func TestComposeLoadsEnvFile(t *testing.T) {
	compose := repoFile(t, "docker-compose.yml")

	appIdx := strings.Index(compose, "\n  app:\n")
	require.GreaterOrEqual(t, appIdx, 0, "compose 里必须有 app 服务")

	rest := compose[appIdx:]
	nginxIdx := strings.Index(rest, "\n  nginx:")
	require.Greater(t, nginxIdx, 0)
	appBlock := rest[:nginxIdx]

	require.Contains(t, appBlock, "env_file:",
		"app 必须用 env_file 整份加载 .env —— 逐条手抄 environment 必然漂移(见 docs/configuration.md §4.1)")
	require.Contains(t, appBlock, "- .env", "env_file 应当指向 .env")
}

// TestSMTPNamingIsConsistent 锁死 SMTP 命名统一。
//
// config 读 SMTP_USER(与 SMTP_PASSWORD、SMTP_TLS 成对),compose 曾经传的
// 却是 SMTP_USERNAME —— 两个名字只差一半,而静态检查照着 bug 写了白名单,
// 于是这个错误活到了部署那天。
func TestSMTPNamingIsConsistent(t *testing.T) {
	require.True(t, envExampleKeys(t)["SMTP_USER"], "SMTP_USER 必须列在 .env.example")
	require.False(t, envExampleKeys(t)["SMTP_USERNAME"],
		"SMTP_USERNAME 是废弃名,config 读的是 SMTP_USER —— 两处并存会让人改错那个")

	require.NotContains(t, repoFile(t, "docker-compose.yml"), "SMTP_USERNAME",
		"compose 不再逐条透传 SMTP 变量(env_file 直达),也不该残留废弃名")
	require.NotContains(t, repoFile(t, "internal/deploycheck/compose_integration_test.go"),
		`"SMTP_USERNAME": true`,
		"测试白名单里不许再出现 SMTP 废弃名的放行条目:那是照着 bug 写的")
}

// TestSecondsVarsAcceptBareNumbers 覆盖 LOGIN_LOCK_SECONDS 这一类踩过的坑。
//
// 这些变量名字里写着 SECONDS,文档给的也是裸数字,而 time.ParseDuration("900")
// 会因为缺单位直接报错 —— 以前用 r.duration 读它们,照文档填反而启动失败。
func TestSecondsVarsAcceptBareNumbers(t *testing.T) {
	baseEnv(t)

	t.Setenv("LOGIN_LOCK_SECONDS", "900")
	t.Setenv("MAIL_VERIFY_COOLDOWN_SECONDS", "60")

	cfg, err := config.Load()
	require.NoError(t, err, "裸数字必须被接受:文档与 .env.example 给的就是裸数字")
	require.Equal(t, 900*time.Second, cfg.Auth.LoginLockDuration)
	require.Equal(t, 60*time.Second, cfg.Mail.VerifyCooldown)
}

// TestSecondsVarsStillAcceptDurations 是兼容性要求:带单位的写法不能突然变错,
// 存量 .env 里可能已经写成 15m。
func TestSecondsVarsStillAcceptDurations(t *testing.T) {
	baseEnv(t)

	t.Setenv("LOGIN_LOCK_SECONDS", "15m")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 15*time.Minute, cfg.Auth.LoginLockDuration)
}
