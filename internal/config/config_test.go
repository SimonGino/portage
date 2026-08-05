package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/config"
)

func TestLoadFallsBackToDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("配置文件缺失时不应报错: %v", err)
	}
	if cfg != config.Default() {
		t.Errorf("cfg = %+v, 期望全默认值 %+v", cfg, config.Default())
	}
	if cfg.Listen != "127.0.0.1:8317" {
		t.Errorf("默认 listen = %q, 期望绑回环（M1 前没有网关 key 鉴权）", cfg.Listen)
	}
}

func TestLoadOverridesOnlyWhatIsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("db_path: /tmp/other.db\nlog_bodies: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	if cfg.DBPath != "/tmp/other.db" {
		t.Errorf("db_path = %q", cfg.DBPath)
	}
	if !cfg.LogBodies {
		t.Error("log_bodies 未生效")
	}
	if cfg.Listen != config.Default().Listen {
		t.Errorf("未设置的 listen 被改成 %q, 期望保持默认", cfg.Listen)
	}
	if cfg.DefaultMaxTokens != config.Default().DefaultMaxTokens {
		t.Errorf("未设置的 default_max_tokens 被改成 %d", cfg.DefaultMaxTokens)
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: [not, a, string\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Load(path); err == nil {
		t.Error("非法 YAML 未报错")
	}
}
