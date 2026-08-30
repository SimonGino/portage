// Package mail 是网关唯一的发信出口：从 settings 表读 SMTP 配置、发一封纯文本邮件。
//
// 选 wneessen/go-mail 而不是 net/smtp（#69 调研）：标准库 frozen、无 AUTH LOGIN、
// 无 465 implicit TLS，且 PlainAuth 硬性拒绝「非 TLS 且非 localhost」的连接——
// 内网明文 relay 场景标准库无解，这是换库的决定性理由。
//
// 每次发信都现读配置、现建客户端：口径要求「改完即生效」（#62 决议 7），缓存一个
// 连接就得再修一套失效逻辑，而发信频率是「注册一次发一封」量级，不值。
package mail

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/SimonGino/portage/internal/store"

	gomail "github.com/wneessen/go-mail"
)

// 加密三态（#69 调研的映射）：starttls→TLSMandatory（587）、ssl→465 implicit TLS、
// none→明文（25/自定义端口，配合 *NoEnc 档 AUTH 才能在明文内网 relay 上带凭证）。
const (
	EncryptionSTARTTLS = "starttls"
	EncryptionSSL      = "ssl"
	EncryptionNone     = "none"
)

// Config 是一次发信要的全部参数，与 settings 表那六个键一一对应。
type Config struct {
	Host       string
	Port       int
	Encryption string
	Username   string
	Password   string
	From       string
}

// Configured 判 SMTP 配没配好。判据是 host 与发件地址都在——端口有默认值、
// 加密有默认档、匿名 relay 不用凭证，只有这两样是缺了就发不出去的。
func (c Config) Configured() bool {
	return c.Host != "" && c.From != ""
}

// LoadConfig 从 settings 表读 SMTP 配置。没配过的键读出来是空串/零值，
// Configured 据此判「未配」——注册入口关不关就看它。
func LoadConfig(ctx context.Context, db store.Queryer) (Config, error) {
	var c Config
	for key, dst := range map[string]*string{
		store.SettingSMTPHost:       &c.Host,
		store.SettingSMTPEncryption: &c.Encryption,
		store.SettingSMTPUsername:   &c.Username,
		store.SettingSMTPPassword:   &c.Password,
		store.SettingSMTPFrom:       &c.From,
	} {
		v, err := store.GetSetting(ctx, db, key)
		if err != nil {
			return Config{}, err
		}
		*dst = v
	}
	port, err := store.GetSetting(ctx, db, store.SettingSMTPPort)
	if err != nil {
		return Config{}, err
	}
	if port != "" {
		if c.Port, err = strconv.Atoi(port); err != nil {
			// 端口写坏只能来自手写 SQL（管理端保存时校验过）。当没配处理，
			// 让默认端口顶上，比让每封邮件带着一个解释不了的错误强。
			c.Port = 0
		}
	}
	return c, nil
}

// Send 发一封纯文本邮件。错误原文可能含 SMTP 服务器的应答，但 go-mail 不会把
// 密码写进错误——这里也不补，secret 不回显的口径对发信错误同样成立。
func Send(ctx context.Context, cfg Config, to, subject, body string) error {
	opts := []gomail.Option{gomail.WithTimeout(gomail.DefaultTimeout)}
	switch cfg.Encryption {
	case EncryptionSSL:
		// 465 implicit TLS。WithSSLPort(false)：端口未配时用默认 465，不回落明文。
		opts = append(opts, gomail.WithSSLPort(false))
	case EncryptionNone:
		opts = append(opts, gomail.WithTLSPortPolicy(gomail.NoTLS))
	default:
		// 默认档 STARTTLS 必须成功（TLSMandatory），失败即断——机会式回落明文会把
		// 凭证发在明文连接上，而人配的明明是加密档。
		opts = append(opts, gomail.WithTLSPortPolicy(gomail.TLSMandatory))
	}
	if cfg.Port > 0 {
		opts = append(opts, gomail.WithPort(cfg.Port))
	}
	if cfg.Username != "" {
		auth := gomail.SMTPAuthAutoDiscover
		if cfg.Encryption == EncryptionNone {
			// 明文连接上 go-mail 的常规 AUTH 档拒发凭证（防中间人骗密码），内网
			// relay 要带凭证只有 *NoEnc 档这一条路——这正是换掉 net/smtp 的理由。
			auth = gomail.SMTPAuthPlainNoEnc
		}
		opts = append(opts, gomail.WithSMTPAuth(auth),
			gomail.WithUsername(cfg.Username), gomail.WithPassword(cfg.Password))
	}
	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return fmt.Errorf("建 SMTP 客户端: %w", err)
	}
	msg := gomail.NewMsg()
	if err := msg.From(cfg.From); err != nil {
		return fmt.Errorf("发件地址不合法: %w", err)
	}
	if err := msg.To(to); err != nil {
		return fmt.Errorf("收件地址不合法: %w", err)
	}
	msg.Subject(subject)
	msg.SetBodyString(gomail.TypeTextPlain, body)
	return client.DialAndSendWithContext(ctx, msg)
}

// Sender 是发信的函数面。admin 包持它而不是直接调 Send，测试才能把真 SMTP 换成
// 记录桩——注册/验证/重置的流程测试不该依赖一台邮件服务器。
type Sender func(ctx context.Context, db *sql.DB, to, subject, body string) error

// DefaultSender 是生产实现：现读配置、现发。未配时报错——调用方该在发之前就用
// Configured 挡掉，走到这儿还未配是流程漏了闸。
func DefaultSender(ctx context.Context, db *sql.DB, to, subject, body string) error {
	cfg, err := LoadConfig(ctx, db)
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return fmt.Errorf("SMTP 未配置")
	}
	return Send(ctx, cfg, to, subject, body)
}
