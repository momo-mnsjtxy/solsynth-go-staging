// SPDX-License-Identifier: AGPL-3.0-only
// 后端入口：独立管理命令先分派；常规启动按“解析 → 校验 → 连接 → 组装 → 服务”执行。
// 查看参数：go run ./cmd/backend --help
// 执行原有离线管理命令：go run ./cmd/backend initialize-federation-identities --help
// 不建立生产表、账户、权限或密钥；启动资源随本次命令退出按逆序释放。
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/application"
)

// run 是唯一启动流程；离线配置校验与独立管理命令都不会进入数据库连接阶段。
func run(args []string, out io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "revoke-federation-public-admission":
			return revokeFederationPublicAdmission(args[1:], out)
		case "initialize-federation-public-admission":
			return initializeFederationPublicAdmission(args[1:], out)
		case "initialize-federation-server":
			return initializeFederationServer(args[1:], out)
		case "initialize-federation-identities":
			return initializeFederationIdentities(args[1:], out)
		}
	}
	options, err := parseBackendOptions(args, out) // 参数只解析一次，后续阶段共享同一份结果。
	if err != nil {
		return err
	}
	startup := backendStartup{options: options}
	defer startup.closeConfiguredResources() // 即使后续策略校验失败，也释放已打开的媒体目录和 PIN 密钥。
	if err = startup.validateConfiguration(); err != nil {
		return err
	}
	if !options.serve {
		return startup.describeOfflineConfiguration(out) // --serve 是数据库连接和监听的唯一开关。
	}

	db, err := sql.Open("pgx", startup.dsn) // sql.Open 只配置连接池；网络检查在组装之后执行。
	if err != nil {
		return errors.New("database configuration unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	backend, err := application.NewPostgres(db, nil, startup.config) // 只组装显式配置的领域能力。
	if err != nil {
		return errors.New("backend configuration unavailable")
	}
	return startup.serve(db, backend, out) // 数据库就绪、后台任务、HTTP 服务及信号退出都属于本次服务生命周期。
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "backend:", err)
		os.Exit(1)
	}
}
