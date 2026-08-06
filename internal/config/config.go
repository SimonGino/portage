// Package config loads the minimal startup configuration.
//
// 业务配置（渠道 / 纳管模型 / 接入点 / 候选 / 凭证）全部落 DB，不在这里。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	DBPath string `yaml:"db_path"`
	// LogBodies is the troubleshooting switch; bodies stay out of logs by default.
	LogBodies bool `yaml:"log_bodies"`

	// 以下字段 M0 接受但不使用，留给后续里程碑。
	AdminPassword    string `yaml:"admin_password"`
	DefaultMaxTokens int    `yaml:"default_max_tokens"`
	RateLimitQPS     int    `yaml:"rate_limit_qps"`
	RateLimitBurst   int    `yaml:"rate_limit_burst"`
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
	return cfg, nil
}
