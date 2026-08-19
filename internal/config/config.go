// Package config loads the minimal startup configuration.
//
// 业务配置（渠道 / 纳管模型 / 接入点 / 候选 / 凭证）全部落 DB，不在这里。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	DBPath string `yaml:"db_path"`
	// LogBodies is the troubleshooting switch; bodies stay out of logs by default.
	LogBodies bool `yaml:"log_bodies"`

	// Retry 是同候选退避重试（口径层 v0.19）。max_retries 配 0 即关闭，行为回到 M0。
	Retry Retry `yaml:"retry"`

	// Queue 是渠道并发闸的有界排队参数（口径层 v0.50）。全局两项、不进渠道表——
	// 渠道表只填「限多少」（max_concurrency），排队策略是网关行为不是渠道属性。
	Queue Queue `yaml:"concurrency_queue"`

	// AdminPassword 只用来**初始化**管理端密码（口径层 §2.7：登录后可改，改后配置项失效）。
	// 可以用环境变量 PORTAGE_ADMIN_PASSWORD 覆盖，见 Load。
	AdminPassword string `yaml:"admin_password"`

	// RateLimitQPS / RateLimitBurst 是全局令牌桶（口径层 §2.7，v0.15 定 10/20）。
	// **配 0 即关闭限流**；只配了 qps 时 burst 由 newLimiter 兜底。
	// v0.81 起桶是两只（生成面一只、count_tokens 一只，见 server.pickLimiter），
	// 这一份配置同时是两只桶的参数——所以最坏总速率是 2× 这里写的数。
	RateLimitQPS   int `yaml:"rate_limit_qps"`
	RateLimitBurst int `yaml:"rate_limit_burst"`

	// CallLogRetentionDays 是流水保留期（#35，v0.93 定 90 天）：启动时清一次、
	// 之后每天清一次 90 天前的 call_logs。**配 0 即永久保留**——零值语义与
	// rate_limit_qps 同一个形状，Load 里不得补零值，否则「写了 0」被悄悄改回 90，
	// 而删掉的流水回不来。
	CallLogRetentionDays int `yaml:"call_log_retention_days"`

	// M0 接受但不使用，留给后续里程碑。
	DefaultMaxTokens int `yaml:"default_max_tokens"`
}

// Retry 的默认值见 Default()。MaxRetries 是**重试**次数，不含首次尝试。
//
// MaxAttempts 与它是两层，不是一件事（口径层 v0.38）：MaxRetries 管同一份凭证上的
// 抖动重试，MaxAttempts 管一次请求最多打多少次上游、跨凭证累计。两层都要——只留
// 内层，最坏耗时随凭证数线性增长（凭证是运营数据随时会加，配置里没有任何地方提示
// 「加第 6 把会让超时翻倍」）；只留外层且跨凭证共享一份预算，则会出现「换到第二份
// 时预算耗尽、第三份根本没试过」，那份是好的却没被用上。
type Retry struct {
	MaxRetries int `yaml:"max_retries"`
	// MaxAttempts 是一次请求的全局上游尝试上限，跨凭证累计；写 0 即不封顶。
	MaxAttempts int           `yaml:"max_attempts"`
	BaseDelay   time.Duration `yaml:"base_delay"`
	MaxDelay    time.Duration `yaml:"max_delay"`
}

// Queue 的默认值见 Default()。只对设了 max_concurrency 的渠道生效。
type Queue struct {
	// Factor 是队列上限系数：队列上限 = 渠道并发上限 × factor（口径层 v0.50 默认
	// ×1）。**显式写 0 = 不排队**，闸满立即回 429（queue_full）。
	Factor int `yaml:"factor"`
	// Wait 是排队等待超时，到点回 429（queue_timeout）。<=0 时兜底默认值。
	Wait time.Duration `yaml:"wait"`
	// RetryAfter 是队满/超时那两种 429 的 Retry-After 值。<=0 时兜底默认值。
	RetryAfter time.Duration `yaml:"retry_after"`
}

// Default binds to loopback only as the conservative default; deployments that
// want LAN access opt in with listen: "0.0.0.0:8317" (relay has key auth and
// the admin UI has session auth, so exposing it is a deliberate choice, not a leak).
func Default() Config {
	return Config{
		Listen:           "127.0.0.1:8317",
		DBPath:           "./gateway.db",
		DefaultMaxTokens: 8192,
		RateLimitQPS:     10,
		RateLimitBurst:   20,
		// 90 天覆盖「回头看上一季度用量」的个人需求；再长的账不如导出来另存。
		CallLogRetentionDays: 90,
		// 取值参照 M0 实测（portage-legacy#6）：Codex 对 5xx 的退避是 0.22→0.45→0.84→1.62s，
		// 量级相仿。重试 2 次是「够救瞬时限流、又不至于让客户端干等太久」的折中；
		// 真实 429 通常带 Retry-After，那时以它为下界。
		Retry: Retry{MaxRetries: 2, MaxAttempts: 6, BaseDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second},
		// 口径层 v0.50 定的三个默认：队列 = 上限 ×1、等 30s、Retry-After 10s。
		Queue: Queue{Factor: 1, Wait: 30 * time.Second, RetryAfter: 10 * time.Second},
	}
}

// Load reads path, falling back to Default when the file does not exist.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		// 这条早退也要过 applyEnv：`docker run` 不挂配置文件是常态，
		// 那时 env 是设密码的唯一途径。
		applyEnv(&cfg)
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Listen == "" {
		cfg.Listen = Default().Listen
	}
	if cfg.DBPath == "" {
		cfg.DBPath = Default().DBPath
	}
	// rate_limit_qps 与 max_retries 同理，不在这里兜底：整块缺席 → 保持默认 10；
	// 显式写 0 → 就是要关掉限流。补零值会让「写了 0」被悄悄改回 10，关不掉。
	//
	// max_retries 这里不兜底，靠 Unmarshal 覆盖在 Default() 之上的语义区分两种情况：
	// 整个 retry 块缺席 → 保持默认 2；显式写 max_retries: 0 → 就是要关掉重试。
	// 若在这里补零值，「写了 0」会被悄悄改回 2，关不掉。max_attempts 同一个陷阱、
	// 同样不兜底：写 0 就是「不封顶」，补回 6 会让人以为封不掉。
	// 两个 delay 反过来必须兜底，否则只写了 max_retries 时退避退了个寂寞。
	if cfg.Retry.BaseDelay <= 0 {
		cfg.Retry.BaseDelay = Default().Retry.BaseDelay
	}
	if cfg.Retry.MaxDelay <= 0 {
		cfg.Retry.MaxDelay = Default().Retry.MaxDelay
	}
	// factor 与 max_retries 同一个道理不兜底：显式写 0 = 闸满不排队立即拒。
	// 两个时长则必须兜底——wait 停在 0 会让每个排队请求当场超时，而那看起来
	// 像闸坏了，不像配置漏了。
	if cfg.Queue.Wait <= 0 {
		cfg.Queue.Wait = Default().Queue.Wait
	}
	if cfg.Queue.RetryAfter <= 0 {
		cfg.Queue.RetryAfter = Default().Queue.RetryAfter
	}
	applyEnv(&cfg)
	return cfg, nil
}

// applyEnv 目前只有管理端密码这一项走环境变量。
//
// 加它是为了容器：config.docker.yaml 是**烤进镜像**的，把密码写在里面等于写进
// 镜像层历史；而为了设一个密码去挂一份配置文件，是在最常见的路径上要求最麻烦的
// 操作。密码属于凭证，凭证走 env 是容器里的常规做法。
//
// 优先级 env > 文件：env 是部署时才知道的，文件是仓库里带着的。
// 空串不算设置——`PORTAGE_ADMIN_PASSWORD=` 与没写它是一回事，不该把文件里的值清掉。
//
// 注意这仍然只是**初始化**：库里已经有密码了，这里给什么都不生效（见 admin.Bootstrap）。
func applyEnv(cfg *Config) {
	if v := os.Getenv("PORTAGE_ADMIN_PASSWORD"); v != "" {
		cfg.AdminPassword = v
	}
}
