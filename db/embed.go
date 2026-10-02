// Package db 承载数据库相关的迁移 SQL 与 sqlc 查询定义的嵌入入口。
//
// 迁移文件用 go:embed 编进二进制(ADR-006 的同一思路),部署时只需一个
// 可执行文件,不必额外拷贝 SQL 目录。需要「改磁盘上的 SQL 再重跑」的
// 运维场景可以用 MIGRATIONS_DIR 环境变量覆盖,见 internal/platform/db。
package db

import "embed"

// FS 包含全部迁移与查询定义文件。
//
//go:embed migrations/*.sql queries/*.sql
var FS embed.FS
