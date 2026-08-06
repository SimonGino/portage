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

	// 以下字段 M0 接受但不使用，留给后续里程碑。
	AdminPassword    string `yaml:"admin_password"`
	DefaultMaxTokens int    `yaml:"default_max_tokens"`
	RateLimitQPS     int    `yaml:"rate_limit_qps"`
	RateLimitBurst   int    `yaml:"rate_limit_burst"`
}

// Retry 的默认值见 Default()。MaxRetries 是**重试**次数，不含首次尝试。
type Retry struct {
	MaxRetries int           `yaml:"max_retries"`
	BaseDelay  time.Duration `yaml:"base_delay"`
	MaxDelay   time.Duration `yaml:"max_delay"`
}

// Default binds to loopback only: there is no gateway key auth until M1, so a
// stray 0.0.0.0 would put an unauthenticated relay on the network.
func Default() Config {
	return Config{
		Listen:           "127.0.0.1:8317",
		DBPath:           "./gateway.db",
		DefaultMaxTokens: 8192,
		RateLimitQPS:     10,
		RateLimitBurst:   20,
		// 取值参照 M0 实测（#6）：Codex 对 5xx 的退避是 0.22→0.45→0.84→1.62s，
		// 量级相仿。重试 2 次是「够救瞬时限流、又不至于让客户端干等太久」的折中；
		// 真实 429 通常带 Retry-After，那时以它为下界。
		Retry: Retry{MaxRetries: 2, BaseDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second},
	}
}

// Load reads path, falling back to Default when the file does not exist.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
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
	// max_retries 这里不兜底，靠 Unmarshal 覆盖在 Default() 之上的语义区分两种情况：
	// 整个 retry 块缺席 → 保持默认 2；显式写 max_retries: 0 → 就是要关掉重试。
	// 若在这里补零值，「写了 0」会被悄悄改回 2，关不掉。
	// 两个 delay 反过来必须兜底，否则只写了 max_retries 时退避退了个寂寞。
	if cfg.Retry.BaseDelay <= 0 {
		cfg.Retry.BaseDelay = Default().Retry.BaseDelay
	}
	if cfg.Retry.MaxDelay <= 0 {
		cfg.Retry.MaxDelay = Default().Retry.MaxDelay
	}
	return cfg, nil
}
