package main

import (
	"runtime/debug"
	"strings"
)

// 这些值由构建时 -ldflags "-X main.version=... -X main.commit=... -X main.buildTime=..." 注入。
var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

// versionString 组装版本信息。未注入提交号时回退到 Go 自身记录的 vcs 信息,
// 保证 `go run` 场景下也能看出代码来源。
func versionString() string {
	parts := []string{version}
	if commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && s.Value != "" {
					commit = s.Value
					if len(commit) > 7 {
						commit = commit[:7]
					}
					break
				}
			}
		}
	}
	if commit != "" {
		parts = append(parts, "commit="+commit)
	}
	if buildTime != "" {
		parts = append(parts, "built="+buildTime)
	}
	return strings.Join(parts, " ")
}
