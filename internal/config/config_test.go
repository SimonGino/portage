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

// max_retries 的两种「0」必须分得开：没写 retry 块是「用默认」，显式写 0 是「关掉」。
// 混淆的后果是关不掉重试，而 #13 把「配 0 时行为与 M0 完全一致」定成了回归护栏。
func TestLoadDistinguishesAbsentRetryFromExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{name: "整块缺席", yaml: "db_path: /tmp/a.db\n", want: config.Default().Retry.MaxRetries},
		{name: "块在但没写 max_retries", yaml: "retry:\n  base_delay: 1s\n", want: config.Default().Retry.MaxRetries},
		{name: "显式关闭", yaml: "retry:\n  max_retries: 0\n", want: 0},
		{name: "显式调大", yaml: "retry:\n  max_retries: 5\n", want: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("加载失败: %v", err)
			}
			if cfg.Retry.MaxRetries != tc.want {
				t.Errorf("max_retries = %d, 期望 %d", cfg.Retry.MaxRetries, tc.want)
			}
			// 两个退避间隔无论如何都得有值，否则退避退了个寂寞。
			if cfg.Retry.BaseDelay <= 0 || cfg.Retry.MaxDelay <= 0 {
				t.Errorf("退避间隔 = %v / %v, 都必须为正", cfg.Retry.BaseDelay, cfg.Retry.MaxDelay)
			}
		})
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
