package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yggauth/yggauth/internal/bootstrap"
	"github.com/yggauth/yggauth/internal/config"
	"github.com/yggauth/yggauth/internal/platform/log"
)

// runAdmin 处理 `yggauth admin ...` 子命令。
//
// 存在意义:首启引导默认**不会**复活被删掉的管理员(见 internal/bootstrap
// 包注释),所以「管理员删光了」这个状态必须有一个显式的、运维主动发起的
// 出口。除此之外它也用于关掉 ADMIN_BOOTSTRAP 的部署里手工建号。
func runAdmin(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "create":
		return runAdminCreate(args[1:])
	default:
		return usageError()
	}
}

func usageError() error {
	return fmt.Errorf("未知 admin 动作,可用:\n  yggauth admin create [-email 地址] [-username 名] [-password-stdin] [-must-change=false]")
}

func runAdminCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	email := fs.String("email", "", "管理员邮箱(默认取 ADMIN_EMAIL)")
	username := fs.String("username", "", "用户名(默认取 ADMIN_USERNAME)")
	password := fs.String("password", "", "初始密码;留空则随机生成(不推荐用它:会进 shell 历史与进程列表)")
	passwordStdin := fs.Bool("password-stdin", false, "从标准输入读初始密码,避免出现在 shell 历史与进程列表")
	mustChange := fs.Bool("must-change", true, "要求首登改密(默认开启)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := log.New(log.Options{Level: cfg.Log.Level, Format: cfg.Log.Format})

	if *email == "" {
		*email = cfg.Bootstrap.Email
	}
	if *username == "" {
		*username = cfg.Bootstrap.Username
	}
	pwd := *password
	if *passwordStdin {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("读取标准输入失败: %w", err)
		}
		pwd = strings.TrimRight(string(raw), "\r\n")
	}

	ctx := context.Background()
	pool, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer pool.Close()

	out, err := bootstrap.Create(ctx, pool, newHasher(cfg), bootstrap.Options{
		Enabled:      true,
		Email:        *email,
		Username:     *username,
		Password:     pwd,
		Policy:       newPolicy(cfg),
		MustChange:   *mustChange,
		LoginEnabled: cfg.Auth.MCLoginDefault,
	})
	if err != nil {
		return err
	}

	if out.Kind != bootstrap.KindCreated {
		fmt.Printf("未创建:%s\n", out.Reason)
		return nil
	}

	fmt.Printf("已创建平台管理员:%s <%s>\n", out.Username, out.Email)
	if out.GeneratedPassword != "" {
		// 与启动引导同一处理:一次性、stderr、不进结构化日志
		printGeneratedAdmin(out.Username, out.Email, out.GeneratedPassword)
	} else if *mustChange {
		fmt.Println("该账号首登会被要求修改密码。")
	}
	return nil
}
