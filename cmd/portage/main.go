// Command gateway is the single binary: load config, open the database, apply
// the startup gate, then serve.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SimonGino/portage/internal/admin"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/server"
	"github.com/SimonGino/portage/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "启动配置文件路径，缺失时全用默认值")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(*configPath, log); err != nil {
		log.Error("gateway 启动失败", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := store.Validate(ctx, db); err != nil {
		return err
	}

	// 空 api_keys 表意味着每个转发请求都会 401。按口径层 v0.21 通则这该「启动即报」，
	// 但这里只警告不拒启：干净库第一次起来必然是空的，而配 key 得先有个跑着的网关
	// （开 /admin 或对着它建的库灌 SQL），拒启会把这两条路一起堵死。
	if n, err := store.CountAPIKeys(ctx, db); err != nil {
		return err
	} else if n == 0 {
		log.Warn("api_keys 表是空的，所有转发请求都会回 401；开 /admin 建一把，无 UI 的部署见 scripts/seed-example.sql 的「网关 key」一节")
	}

	// 管理端密码：配置里的明文只用来**初始化**，库里已经有了就一概不动
	// （口径层 §2.7「登录后可改，改后配置项失效」）。同样只警告不拒启——没设密码
	// 只是管理端登不进去，转发照常工作，为它拒启会把纯转发的用法一起卡死。
	if ok, err := admin.Bootstrap(ctx, db, cfg.AdminPassword); err != nil {
		return err
	} else if !ok {
		log.Warn("未设置管理密码，管理端无法登录；在 config.yaml 里填 admin_password 后重启")
	}

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.New(cfg, db, log).Engine(),
		// 不设 WriteTimeout：它会掐断长 SSE 流。写超时改由 relay 用
		// http.NewResponseController(w).SetWriteDeadline 逐次推进。
		ReadHeaderTimeout: 20 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway 已启动", "listen", cfg.Listen, "db", cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("收到退出信号，等待在途请求结束")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
