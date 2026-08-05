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

	"github.com/SimonGino/ai-gateway/internal/config"
	"github.com/SimonGino/ai-gateway/internal/server"
	"github.com/SimonGino/ai-gateway/internal/store"
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
