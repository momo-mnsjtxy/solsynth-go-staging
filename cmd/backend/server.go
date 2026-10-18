// SPDX-License-Identifier: AGPL-3.0-only
// 服务生命周期：检查显式数据库/表、安装已选语料、启动任务和 HTTP，并在信号后有界退出。
// 查看运行参数：go run ./cmd/backend --help
// 校验服务相关边界：go test ./cmd/backend -run 'TestStartup|TestBotRenewal'
// 不运行迁移；通知、结算和联邦维护继续使用各自的显式周期。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"solsynth.local/go-migration/internal/application"
)

// serve 只接收已组装的后端，校验数据库后才打开监听和后台任务。
func (s *backendStartup) serve(db *sql.DB, backend *application.Backend, out io.Writer) error {
	options := s.options
	// 信号取消会停止所有周期任务；HTTP 排空另用独立的五秒期限。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 5*time.Second) // 连接与表检查共享一次有界就绪期限。
	err := db.PingContext(check)
	if err == nil {
		err = backend.Ready(check)
	}
	cancel()
	if err != nil {
		return errors.New("required database/schema unavailable; startup performs no migrations")
	}
	// 仅安装刚才校验的显式语料，不初始化其他业务状态。
	if s.fortuneCorpus != nil {
		install, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = backend.InstallFortuneCorpus(install, *s.fortuneCorpus)
		cancel()
		if err != nil {
			return errors.New("explicit fortune corpus configuration failed")
		}
	}
	// 数据库和显式语料准备好之后才开放 HTTP，避免外部请求看到半初始化状态。
	listener, err := net.Listen("tcp", options.listen)
	if err != nil {
		return errors.New("backend listener unavailable")
	}
	defer listener.Close()
	go func() {
		_ = backend.RunNotifications(ctx, func(code string) { fmt.Fprintln(out, "Notification worker:", code) })
	}()

	// 呼叫/联邦撤销不能排在慢速结算后面，因此使用独立周期和超时。
	if *options.externalSettings.calls || *options.externalSettings.federation || *options.externalSettings.streams || *options.awardSettings.maintenance {
		go runExternalMaintenance(ctx, backend, *options.externalSettings.maintenance, out)
	}
	options.fortuneSettings.start(ctx, backend, out)
	options.publisherSettings.start(ctx, backend, out)
	options.postAwardSettings.start(ctx, backend, out)
	if s.config.BotExecution != nil {
		if err := startBotRenewalWorker(ctx, backend, options.botRenewalInterval, out); err != nil {
			return err
		}
	}
	// 保留既有请求限制：头部 5s、读取 20s、写出 30s、空闲 60s、头部至多 32KiB。
	server := &http.Server{Handler: backend, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go runLifecycleMaintenance(ctx, backend, options.settlementInterval, out)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintf(out, "Authenticated backend listening on %s\n", listener.Addr())
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("backend HTTP server stopped unexpectedly")
	}
	return nil
}

// runExternalMaintenance 独立处理呼叫/联邦恢复，使撤销不被钱包结算拖延。
func runExternalMaintenance(ctx context.Context, backend *application.Backend, interval time.Duration, out io.Writer) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, cancel := context.WithTimeout(ctx, 20*time.Second) // 每批最多 100 条，超时后交给持久状态继续恢复。
			_, err := backend.ProcessExternalDue(job, 100)
			cancel()
			if err != nil {
				fmt.Fprintln(out, "Call/federation maintenance batch failed; recovery state remains durable.")
			}
		}
	}
}

// runLifecycleMaintenance 同轮先处理钱包生命周期，再清理持久媒体预留。
func runLifecycleMaintenance(ctx context.Context, backend *application.Backend, interval time.Duration, out io.Writer) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, cancel := context.WithTimeout(ctx, 20*time.Second) // 每批最多 100 条，超时后交给持久状态继续恢复。
			_, err := backend.ProcessDue(job, 100)
			cancel()
			if err != nil {
				fmt.Fprintln(out, "Lifecycle/projection batch failed; committed state remains durable.")
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 20*time.Second) // 清理另开期限，结算失败也继续回收媒体。
			_, cleanupErr := backend.CleanupMedia(cleanupCtx, 100)
			cleanupCancel()
			if cleanupErr != nil {
				fmt.Fprintln(out, "Media cleanup batch failed; durable reservations retained.")
			}
		}
	}
}
