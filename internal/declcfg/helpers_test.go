package declcfg_test

import (
	"io"
	"log/slog"
	"strings"

	"github.com/SimonGino/portage/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func textLogger(w *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}

// loadProcessConfig 存在只为一件事：让「config.yaml 保持宽松」这条口径在 declcfg 的
// 用例里有一个断言点。两份文件的严格度差异是 declcfg 的立身之本，而守着它的用例放在
// 这边比放在 internal/config 那边更容易被下一个人读到。
func loadProcessConfig(path string) (config.Config, error) {
	return config.Load(path)
}
