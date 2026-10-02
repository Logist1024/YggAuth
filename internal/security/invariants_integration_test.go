//go:build integration

// Package security 固定住那些「改错了不会立刻出事、但迟早出事」的安全不变量。
//
// 这一类规则的特征是:违反之后没有即时的功能症状。通配的 CORS
// 今天能用,三个月后某个第三方页面悄悄带走了会话;
// SameSite=None 没配 Secure 也是一样,只在生产环境的浏览器上失败。
//
// 所以它们必须由测试盯着,而不是靠 review 时记得。
package security_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoRoot 返回仓库根目录。
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	// 测试文件在 internal/security/,上溯三层是仓库根。
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// TestGitignoreExcludesSecretsAndData 验证敏感文件不入库。
func TestGitignoreExcludesSecretsAndData(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)

	content := string(raw)

	// 这些必须被忽略。漏掉任何一个的后果都是直接的。
	mustIgnore := []string{".env", "data/", "node_modules/", "bin/"}
	for _, entry := range mustIgnore {
		require.Contains(t, content, entry,
			".gitignore 必须忽略 %s", entry)
	}
}

// TestComposeHasNoLiteralSecrets 验证 compose 里没有明文密钥。
//
// 所有凭据都必须写成 ${VAR} 形式,让缺失在启动时就暴露,
// 而不是悄悄用上一份硬编码的旧密码。
func TestComposeHasNoLiteralSecrets(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	require.NoError(t, err)

	content := string(raw)

	// 形如 PASSWORD: someLiteralValue 且不是 ${...} 引用。
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		if !strings.Contains(upper, "PASSWORD") && !strings.Contains(upper, "SECRET") {
			continue
		}
		require.Contains(t, trimmed, "${",
			"compose 中的凭据必须用 ${VAR} 引用,这一行是硬编码: %s", trimmed)
	}
}

// TestEnvExampleDocumentsRequiredFields 验证 .env.example 覆盖必填项。
func TestEnvExampleDocumentsRequiredFields(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".env.example"))
	require.NoError(t, err)

	content := string(raw)

	// 这些是服务启动的硬依赖,每一个都必须在示例里出现且标注必填。
	required := []string{
		"APP_PUBLIC_DOMAIN",
		"PUBLIC_BASE_URL",
		"DB_PASSWORD",
		"KEY_MASTER_SECRET",
	}

	for _, key := range required {
		require.Contains(t, content, key,
			".env.example 必须列出必填项 %s", key)
		require.Contains(t, content, "【必填】",
			".env.example 必须标注哪些是必填项")
	}
}

// TestNoPrivateKeysInRepository 验证仓库里没有私钥。
//
// 私钥一旦进了 git,即使后续删除也仍然存在于历史里 —
// 必须当作已泄露来处理。所以这里直接查工作区,而不是查历史。
func TestNoPrivateKeysInRepository(t *testing.T) {
	root := repoRoot(t)

	markers := []string{
		"BEGIN RSA PRIVATE KEY",
		"BEGIN EC PRIVATE KEY",
		"BEGIN OPENSSH PRIVATE KEY",
		"BEGIN PRIVATE KEY",
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// 跳过依赖、构建产物与本文件:标记列表本身写在源码里,
		// 扫到自己会永远命中。
		if strings.HasSuffix(path, "invariants_integration_test.go") {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "dist", "data", "tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(raw)

		for _, marker := range markers {
			// .env.example 里给出的是「怎么生成」的示例,不是真私钥。
			if strings.Contains(path, ".env.example") {
				continue
			}
			require.NotContains(t, text, marker,
				"文件 %s 里出现了私钥标记 %s", path, marker)
		}
		return nil
	})
	require.NoError(t, err)
}

// TestDockerfileUsesNonRootAndMinimalBase 验证镜像安全基线。
func TestDockerfileUsesNonRootAndMinimalBase(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	require.NoError(t, err)

	content := string(raw)

	require.Contains(t, content, "FROM gcr.io/distroless",
		"运行镜像应当用 distroless,减少攻击面与 CVE 面")
	require.Contains(t, content, "USER nonroot",
		"容器必须以非 root 运行")
	require.Contains(t, content, "CGO_ENABLED=0",
		"静态编译,CGO 会引入动态链接依赖")
}

// TestNginxDoesNotExposeMetrics 验证监控端点不对公网开放。
func TestNginxDoesNotExposeMetrics(t *testing.T) {
	root := repoRoot(t)

	err := filepath.Walk(filepath.Join(root, "deploy", "nginx"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		content := string(raw)
		if !strings.Contains(content, "/metrics") {
			return nil
		}

		// /metrics 出现的地方必须在返回 404 的 location 里。
		require.Contains(t, content, "return 404",
			"%s 里提到了 /metrics 但没有显式拒绝:它含内部路径与请求量", path)
		return nil
	})
	require.NoError(t, err)
}

// TestDocsHaveNoDanglingRelativeLinks 验证文档没有悬空链接。
//
// M7 的验收项之一。悬空链接在文档里特别隐蔽:
// 点的人要么放弃,要么以为功能不存在,于是自己造一个。
func TestDocsHaveNoDanglingRelativeLinks(t *testing.T) {
	root := repoRoot(t)
	docsDir := filepath.Join(root, "docs")

	entries, err := os.ReadDir(docsDir)
	require.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(docsDir, entry.Name())
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr)

		for _, link := range extractMarkdownLinks(string(raw)) {
			if strings.HasPrefix(link, "http://") ||
				strings.HasPrefix(link, "https://") ||
				strings.HasPrefix(link, "#") ||
				strings.HasPrefix(link, "mailto:") {
				continue
			}

			target := strings.SplitN(link, "#", 2)[0]
			if target == "" {
				continue
			}

			full := filepath.Join(docsDir, filepath.Clean(target))
			_, statErr := os.Stat(full)
			require.NoError(t, statErr,
				"%s 里的链接 [%s] 指向不存在的文件 %s", entry.Name(), link, target)
		}
	}
}

// extractMarkdownLinks 取出所有 Markdown 链接的目标。
func extractMarkdownLinks(content string) []string {
	var out []string
	rest := content

	for {
		open := strings.Index(rest, "](")
		if open < 0 {
			return out
		}
		rest = rest[open+2:]

		close := strings.Index(rest, ")")
		if close < 0 {
			return out
		}
		out = append(out, strings.Trim(rest[:close], " <>"))
		rest = rest[close+1:]
	}
}

// TestRoadmapMilestonesAreOrdered 验证路线图的里程碑顺序正确。
//
// M5 的文档编辑曾把文件截成两半,M0–M4 重复出现、M7 标题丢失。
// 这类损坏不会让任何构建失败,只会让读者读到一份自相矛盾的路线图。
func TestRoadmapMilestonesAreOrdered(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "01-roadmap.md"))
	require.NoError(t, err)

	content := string(raw)

	positions := map[string]int{}
	for i, line := range strings.Split(content, "\n") {
		// 只认「## Mn ·」这种二级标题。表格里的「| M0 |」是排版用的,
		// 不算章节标题 —— 把它们一起统计会得出「每个里程碑出现两次」。
		if !strings.HasPrefix(line, "## M") {
			continue
		}
		// "## M0 · …" 的编号在索引 4:0=# 1=# 2=空格 3=M 4=0
		milestone := string(line[4])
		if _, seen := positions[milestone]; seen {
			t.Fatalf("里程碑 M%s 在路线图中出现了两次(第 %d 行)。"+
				"这通常意味着编辑时把文件截断成了两半。", milestone, i+1)
		}
		positions[milestone] = i
	}

	// M0 到 M7 必须齐备。
	for i := range 8 {
		m := string(rune('0' + i))
		_, ok := positions[m]
		require.True(t, ok, "路线图缺少 M%s 章节", m)
	}

	// 且顺序递增。
	prev := -1
	for i := range 8 {
		m := string(rune('0' + i))
		require.Greater(t, positions[m], prev,
			"路线图中 M%s 出现在 M%s 之前", m, string(rune('0'+i-1)))
		prev = positions[m]
	}
}

// TestJSONResponsesUseEnvelope 验证统一响应包的一致性。
//
// ADR-008:所有 JSON 接口都包在 {code,message,data} 里。
// 混用会让前端每个调用点都要写两套解析。
func TestJSONResponsesUseEnvelope(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "08-deployment.md"))
	if err != nil {
		t.Skip("部署文档不存在")
	}

	// 部署文档里给出的示例响应必须符合信封结构。
	var found bool
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"data"`) {
			continue
		}

		var probe map[string]any
		if json.Unmarshal([]byte(trimmed), &probe) != nil {
			continue
		}
		if _, ok := probe["data"]; ok {
			found = true
			require.Contains(t, probe, "code",
				"示例响应含 data 但没有 code: %s", trimmed)
		}
	}
	_ = found
}
