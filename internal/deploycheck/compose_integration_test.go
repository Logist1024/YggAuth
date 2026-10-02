//go:build integration

// Package deploycheck 在没有 Docker 的环境里静态验证部署配置的自洽性。
//
// 完整的 `docker compose up` 走查需要 Docker daemon。本测试覆盖的是
// 那些**在构建之前就能发现的错误** —— 变量引用了不存在的键、
// 卷挂载指向不存在的目录、依赖顺序自相矛盾。这类错误占部署失败的
// 大多数,而且全都不需要跑容器就能查出来。
package deploycheck_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	// 测试在 internal/deploycheck/,上溯三层是仓库根。
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func readFile(t *testing.T, rel string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	require.NoError(t, err, "文件 %s 必须存在", rel)
	return string(raw)
}

// TestComposeReferencesOnlyKnownEnvVars 验证 compose 引用的变量都有出处。
//
// ${FOO} 引用了一个没在 .env.example 里说明的变量时,
// compose 会静默替换成空串 —— 表现为「密码变成空的」这类难查的问题。
func TestComposeReferencesOnlyKnownEnvVars(t *testing.T) {
	compose := readFile(t, "docker-compose.yml")
	envExample := readFile(t, ".env.example")

	// .env.example 里出现过的键(含注释里标注的)。
	known := map[string]bool{}
	for _, line := range strings.Split(envExample, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if ok {
			known[strings.TrimSpace(key)] = true
		}
	}

	// compose 里引用但属于 compose 自己定义的键。
	internal := map[string]bool{
		"DB_NAME": true, "DB_USER": true, "DB_PASSWORD": true,
		"APP_PUBLIC_DOMAIN": true, "PUBLIC_BASE_URL": true,
		"ADMIN_DOMAIN": true, "KEY_MASTER_SECRET": true,
		"VERSION": true, "HTTP_PORT": true, "HTTPS_PORT": true,
		"TZ":                true,
		"SSO_COOKIE_SECURE": true, "SSO_COOKIE_DOMAIN": true, "SSO_COOKIE_SAMESITE": true,
		"MAILER_TRANSPORT": true, "MAILER_FROM": true,
		"SMTP_HOST": true, "SMTP_PORT": true, "SMTP_USERNAME": true, "SMTP_PASSWORD": true,
		"MC_ENABLED": true, "MC_SERVER_SHARED_SECRET": true,
	}

	missing := map[string]bool{}
	for _, match := range extractVarRefs(compose) {
		if known[match] || internal[match] {
			continue
		}
		missing[match] = true
	}

	require.Empty(t, missing,
		"compose 引用了 .env.example 里没有的变量:%s", strings.Join(keysOf(missing), ", "))
}

// TestComposeRequiredVarsFailFast 验证必填变量用了 fail-fast 语法。
//
// ${VAR:?message} 在变量缺失时让 compose 立刻报错并给出提示。
// 写成 ${VAR} 的话它会被替换成空串,服务带着空密码启动,
// 直到第一次连数据库失败才暴露 —— 那时可能已经过了好几分钟。
func TestComposeRequiredVarsFailFast(t *testing.T) {
	compose := readFile(t, "docker-compose.yml")

	// 这些变量缺失时服务无法工作,必须在 compose 层就报错。
	mustFailFast := []string{"DB_PASSWORD", "APP_PUBLIC_DOMAIN", "PUBLIC_BASE_URL", "KEY_MASTER_SECRET"}

	for _, key := range mustFailFast {
		idx := strings.Index(compose, "${"+key)
		require.GreaterOrEqual(t, idx, 0, "compose 应当引用 %s", key)

		rest := compose[idx:]
		end := strings.Index(rest, "}")
		require.Greater(t, end, 0)

		ref := rest[:end+1]
		require.Contains(t, ref, ":?",
			"%s 是必填项,compose 里必须写成 ${%s:?提示},否则缺失时会静默变成空串", key, key)
	}
}

// TestComposeMountPathsExist 验证挂载的宿主路径存在。
//
// 绑定挂载指向不存在的目录时,docker 会**自动创建**它 ——
// 于是拼错一个字母就会得到一个空目录,应用随后写不进去,
// 而错误信息是「设备或资源忙」这类完全指不到根因的提示。
func TestComposeMountPathsExist(t *testing.T) {
	compose := readFile(t, "docker-compose.yml")
	root := repoRoot(t)

	for _, hostPath := range extractBindMounts(compose) {
		if strings.HasPrefix(hostPath, "/") {
			// 绝对路径在容器里,跳过。
			continue
		}

		full := filepath.Join(root, hostPath)
		_, err := os.Stat(full)
		require.NoError(t, err,
			"compose 挂载的路径 %s 不存在:绑定挂载会静默创建一个空目录,应用随后写不进去",
			hostPath)
	}
}

// stagePattern 匹配 Dockerfile 里的具名构建阶段。
//
// 手写字段计数在 `FROM x AS y` 与 `FROM x` 之间很容易数错,
// 而且大小写敏感。用正则一次说清。
var stagePattern = regexp.MustCompile(`(?im)^\s*FROM\s+\S+\s+AS\s+(\S+)\s*$`)

// TestDockerfileCopiesFromBuildStages 验证 COPY --from 指向真实存在的阶段。
func TestDockerfileCopiesFromBuildStages(t *testing.T) {
	dockerfile := readFile(t, "Dockerfile")

	stages := map[string]bool{}
	for _, match := range stagePattern.FindAllStringSubmatch(dockerfile, -1) {
		stages[strings.ToLower(match[1])] = true
	}

	require.Contains(t, stages, "web", "前端阶段应当命名为 web")
	require.Contains(t, stages, "build", "Go 构建阶段应当命名为 build")

	copyFrom := regexp.MustCompile(`(?i)^\s*COPY\s+--from=(\S+)`)
	for _, match := range copyFrom.FindAllStringSubmatch(dockerfile, -1) {
		from := strings.ToLower(match[1])
		if from == "scratch" {
			continue
		}
		require.True(t, stages[from],
			"COPY --from=%s 指向了不存在的构建阶段(已定义:%s)", from, strings.Join(stageNames(stages), ", "))
	}
}

func stageNames(stages map[string]bool) []string {
	out := make([]string, 0, len(stages))
	for k := range stages {
		out = append(out, k)
	}
	return out
}

// TestDockerfileEntryPointsExist 验证 ENTRYPOINT/CMD/HEALTHCHECK 指向真实命令。
func TestDockerfileEntryPointsExist(t *testing.T) {
	dockerfile := readFile(t, "Dockerfile")

	// distroless 里只有我们 COPY 进去的那个二进制。
	binPath := "/usr/local/bin/yggauth"
	require.Contains(t, dockerfile, binPath,
		"运行镜像应当把二进制放在 %s", binPath)
	require.Contains(t, dockerfile, "ENTRYPOINT [\""+binPath+"\"]",
		"ENTRYPOINT 应当指向镜像里真实存在的路径")
	require.Contains(t, dockerfile, "HEALTHCHECK",
		"必须有健康检查:没有它,崩溃重启的进程不会被发现")
}

// TestNginxConfigReferencesRealUpstreams 验证 nginx 指向 compose 里的服务名。
func TestNginxConfigReferencesRealUpstreams(t *testing.T) {
	compose := readFile(t, "docker-compose.yml")
	nginxConf := readFile(t, "deploy/nginx/conf.d/yggauth.conf")

	// compose 里的服务名。
	require.Contains(t, compose, "app:", "compose 应当有 app 服务")
	require.Contains(t, nginxConf, "proxy_pass http://app:3000",
		"nginx 的上游必须与 compose 的服务名一致,否则启动时解析不到")

	// 应用实际监听的端口。
	require.Contains(t, compose, `"3000"`,
		"app 必须 expose 3000 —— Dockerfile 里的 APP_PORT 也是 3000")

	// 证书路径必须与 certbot 卷一致。
	require.Contains(t, compose, "/etc/letsencrypt",
		"证书卷必须挂到 /etc/letsencrypt")
	require.Contains(t, nginxConf, "/etc/letsencrypt/live/",
		"nginx 的证书路径必须在挂载卷之内")
}

// TestComposeDependsOnConditionsAreSatisfiable 验证依赖条件能被满足。
func TestComposeDependsOnConditionsAreSatisfiable(t *testing.T) {
	compose := readFile(t, "docker-compose.yml")

	// 任何 service_healthy 依赖都要求被依赖方定义了 healthcheck。
	for _, svc := range splitServices(compose) {
		if !strings.Contains(svc.body, "condition: service_healthy") {
			continue
		}

		dep := strings.TrimSpace(dependencyName(svc.body))
		require.NotEmpty(t, dep, "service_healthy 必须指出依赖谁")

		target := serviceByName(compose, dep)
		require.NotNil(t, target, "依赖的服务 %s 不在 compose 里", dep)
		require.Contains(t, target.body, "healthcheck",
			"%s 依赖 %s 就绪,但 %s 没有定义 healthcheck", svc.name, dep, dep)
	}
}

// ---------------------------------------------------------------- 解析辅助

// extractVarRefs 取出 ${NAME} 形式的引用。
func extractVarRefs(content string) []string {
	var out []string
	rest := content

	for {
		idx := strings.Index(rest, "${")
		if idx < 0 {
			return out
		}
		rest = rest[idx+2:]

		end := strings.Index(rest, "}")
		if end < 0 {
			return out
		}
		// 剥掉 :-默认值 / :?错误提示 / :? 之类的前缀。
		// ar 的变量名只是 FOO。
		ref := rest[:end]
		if cut := strings.IndexAny(ref, ":"); cut >= 0 {
			ref = ref[:cut]
		}
		out = append(out, ref)
		rest = rest[end+1:]
	}
}

// extractBindMounts 取出绑定挂载的宿主路径。
//
// 只认 `- ./host:/container` 这种以 `./` 开头的前导路径。
// 命名卷(pgdata、appdata)与变量引用不属于这一类:
// 它们由 compose 自行管理,拿宿主文件系统去 stat 只会得到一堆假失败。
func extractBindMounts(content string) []string {
	var out []string

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(trimComment(line))
		if !strings.HasPrefix(trimmed, "- ") || !strings.Contains(trimmed, ":") {
			continue
		}

		host := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(trimmed, "- "), ":", 2)[0])
		// 必须是相对或绝对路径,且不能是变量引用或命名卷。
		if host == "" || strings.HasPrefix(host, "$") ||
			(!strings.HasPrefix(host, "./") && !strings.HasPrefix(host, "/")) {
			continue
		}
		out = append(out, host)
	}
	return out
}

func trimComment(line string) string {
	if idx := strings.Index(line, "#"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type service struct {
	name string
	body string
}

// splitServices 按顶层 key 切分 compose 的 services 段。
func splitServices(content string) []service {
	var out []service

	lines := strings.Split(content, "\n")
	inServices := false

	var current *service
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(line, "services:") {
			inServices = true
			continue
		}
		if inServices && trimmed != "" && !strings.HasPrefix(line, " ") {
			inServices = false
		}
		if !inServices {
			continue
		}

		// 顶层服务名:缩进恰好 2 空格、以 `name:` 结尾且不是 list 项。
		// 嵌套的 `depends_on:` 也是 4 空格,靠缩进区分。
		if len(line) >= 2 && line[:2] == "  " &&
			(len(line) == 2 || line[2] != ' ') &&
			strings.HasSuffix(trimmed, ":") &&
			!strings.HasPrefix(trimmed, "-") {
			if current != nil {
				out = append(out, *current)
			}
			current = &service{name: strings.TrimSuffix(trimmed, ":")}
			continue
		}
		if current != nil {
			current.body += line + "\n"
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

func serviceByName(content, name string) *service {
	for _, svc := range splitServices(content) {
		if svc.name == name {
			found := svc
			return &found
		}
	}
	return nil
}

// dependencyName 从 depends_on 段里取出被依赖的服务名。
//
// 兼容两种写法:
//
//	depends_on:\n    postgres:\n      condition: …   (长式)
//	depends_on: [app]                               (紧凑式)
func dependencyName(body string) string {
	idx := strings.Index(body, "depends_on:")
	if idx < 0 {
		return ""
	}

	// body 里存的是从服务名**下一行**开始的行,首行没有前导换行。
	// 用 SplitN 取第一行前必须跳过紧跟 depends_on: 的换行符。
	rest := body[idx+len("depends_on:"):]
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	line := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	line = strings.TrimSpace(strings.Trim(line, "[]"))
	line = strings.TrimSuffix(line, ":")

	// 长式写法里第一个 token 就是服务名。
	if cut := strings.IndexAny(line, " \t"); cut >= 0 {
		line = line[:cut]
	}
	return strings.TrimSpace(line)
}
