package main

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

// 升级那一支：收到 upgraded 后 Shutdown 被调、回 errRestart 哨兵。Exec 不进测试。
func TestServeUpgradedShutsDownAndReturnsRestart(t *testing.T) {
	srv := &http.Server{Addr: "127.0.0.1:0"}
	upgraded := make(chan string, 1)
	upgraded <- "0.1.1"

	err := serve(t.Context(), srv, upgraded, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, errRestart) {
		t.Fatalf("serve = %v，期望 errRestart", err)
	}
	// Shutdown 过的 Server 再起一律 ErrServerClosed——这就是「Shutdown 被调」的证据。
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("ListenAndServe after serve = %v，期望 ErrServerClosed", err)
	}
}
