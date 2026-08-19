package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/config"
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
// 混淆的后果是关不掉重试，而 portage-legacy#13 把「配 0 时行为与 M0 完全一致」定成了回归护栏。
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

// max_attempts 的零值陷阱与 max_retries 同（口径层 v0.38）：没写是「用默认 6」，
// 显式写 0 是「不封顶」。补零值的后果比 max_retries 那次更隐蔽——凭证多于 6 份时，
// 想让一次请求把所有凭证都试一遍的人会发现第 7 把永远轮不到，而配置里明明写着 0。
func TestLoadDistinguishesAbsentMaxAttemptsFromExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{name: "整块缺席", yaml: "db_path: /tmp/a.db\n", want: config.Default().Retry.MaxAttempts},
		{name: "块在但没写 max_attempts", yaml: "retry:\n  max_retries: 3\n", want: config.Default().Retry.MaxAttempts},
		{name: "显式不封顶", yaml: "retry:\n  max_attempts: 0\n", want: 0},
		{name: "显式调小", yaml: "retry:\n  max_attempts: 2\n", want: 2},
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
			if cfg.Retry.MaxAttempts != tc.want {
				t.Errorf("max_attempts = %d, 期望 %d", cfg.Retry.MaxAttempts, tc.want)
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

// TestAdminPasswordFromEnv：容器里设密码走 env，不用为一个密码去挂配置文件。
//
// 顺带钉死「空串不算设置」——`PORTAGE_ADMIN_PASSWORD=` 与压根没写是一回事，
// 不该把配置文件里已经写好的值清掉。
func TestAdminPasswordFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("admin_password: from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ env, want string }{
		{"", "from-file"},
		{"from-env", "from-env"},
	} {
		t.Setenv("PORTAGE_ADMIN_PASSWORD", tc.env)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("加载失败: %v", err)
		}
		if cfg.AdminPassword != tc.want {
			t.Errorf("PORTAGE_ADMIN_PASSWORD=%q 时 admin_password = %q, 期望 %q", tc.env, cfg.AdminPassword, tc.want)
		}
	}

	// 没有配置文件时 env 一样管用：镜像里那份 config.docker.yaml 就没写密码。
	t.Setenv("PORTAGE_ADMIN_PASSWORD", "only-env")
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.AdminPassword != "only-env" {
		t.Errorf("配置文件缺席时 admin_password = %q, 期望 only-env", cfg.AdminPassword)
	}
}

// concurrency_queue.factor 的零值陷阱与 max_retries 同：没写是「用默认 ×1」，
// 显式写 0 是「不排队，闸满立即拒」。两个时长反过来必须兜底——wait 停在 0 会让
// 每个排队请求当场超时，看起来像闸坏了，不像配置漏了。
func TestLoadDistinguishesAbsentQueueFactorFromExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{name: "整块缺席", yaml: "db_path: /tmp/a.db\n", want: config.Default().Queue.Factor},
		{name: "块在但没写 factor", yaml: "concurrency_queue:\n  wait: 5s\n", want: config.Default().Queue.Factor},
		{name: "显式不排队", yaml: "concurrency_queue:\n  factor: 0\n", want: 0},
		{name: "显式调大", yaml: "concurrency_queue:\n  factor: 3\n", want: 3},
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
			if cfg.Queue.Factor != tc.want {
				t.Errorf("factor = %d, 期望 %d", cfg.Queue.Factor, tc.want)
			}
			if cfg.Queue.Wait <= 0 || cfg.Queue.RetryAfter <= 0 {
				t.Errorf("wait / retry_after = %v / %v, 都必须为正", cfg.Queue.Wait, cfg.Queue.RetryAfter)
			}
		})
	}
}

// rate_limit_qps 写 0 就是要关掉限流。它与 max_retries 同一个陷阱：在 Load 里
// 「顺手补个零值」会让 0 被悄悄改回默认 10，配置项形同虚设。
func TestRateLimitZeroIsHonoured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yaml  string
		qps   int
		burst int
	}{
		{"整块缺席保持默认", "listen: \":1\"\n", 10, 20},
		{"显式写 0 即关闭", "rate_limit_qps: 0\n", 0, 20},
		{"只改 qps", "rate_limit_qps: 3\n", 3, 20},
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
			if cfg.RateLimitQPS != tc.qps || cfg.RateLimitBurst != tc.burst {
				t.Errorf("qps/burst = %d/%d, 期望 %d/%d",
					cfg.RateLimitQPS, cfg.RateLimitBurst, tc.qps, tc.burst)
			}
		})
	}
}

// call_log_retention_days 的零值陷阱与 rate_limit_qps 同（#35）：没写是「用默认 90 天」，
// 显式写 0 是「永久保留」。补零值的后果是想永久留账的人发现 90 天前的流水被静默删了——
// 而删流水不可逆，这一格混淆的代价比关不掉限流更重。
func TestLoadDistinguishesAbsentRetentionFromExplicitZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{name: "整块缺席", yaml: "db_path: /tmp/a.db\n", want: 90},
		{name: "显式写 0 即永久保留", yaml: "call_log_retention_days: 0\n", want: 0},
		{name: "显式调短", yaml: "call_log_retention_days: 30\n", want: 30},
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
			if cfg.CallLogRetentionDays != tc.want {
				t.Errorf("call_log_retention_days = %d, 期望 %d", cfg.CallLogRetentionDays, tc.want)
			}
		})
	}
}
