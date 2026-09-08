package upstream_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/upstream"
)

// Redact 的纪律（#53）：传输错误经它之后不带上游地址——host、ip、port 一个都不许
// 出现——但要留得住「哪一步、什么原因」，否则排障的那半边信息也没了。

// 真实拨号失败：url.Error 套 net.OpError 套 os.SyscallError。
func TestRedactStripsDialAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close()

	_, err := http.Post(srv.URL+"/v1/messages", "application/json", nil)
	if err == nil {
		t.Fatal("关掉的服务器期望拨不通")
	}
	got := upstream.Redact(err).Error()
	host, port, _ := net.SplitHostPort(addr)
	for _, leak := range []string{srv.URL, addr, host, ":" + port, "/v1/messages"} {
		if strings.Contains(got, leak) {
			t.Errorf("Redact 后仍带 %q：%q", leak, got)
		}
	}
	if !strings.HasPrefix(got, "dial tcp: ") || !strings.Contains(got, "connection refused") {
		t.Errorf("该留得住哪一步与原因：%q", got)
	}
}

// DNS 失败三层套娃：url.Error → net.OpError → net.DNSError，主机名与 DNS 服务器都要摘。
func TestRedactStripsDNSName(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://api.secret.example/v1", Err: &net.OpError{
		Op: "dial", Net: "tcp",
		Err: &net.DNSError{Err: "no such host", Name: "api.secret.example", Server: "10.0.0.53:53", IsNotFound: true},
	}}
	got := upstream.Redact(err).Error()
	if strings.Contains(got, "secret.example") || strings.Contains(got, "10.0.0.53") {
		t.Errorf("Redact 后仍带主机名或 DNS 服务器：%q", got)
	}
	if got != "dial tcp: lookup: no such host" {
		t.Errorf("重组文案 = %q", got)
	}
}

// 证书名不匹配：tls.CertificateVerificationError 套 x509.HostnameError，原文两头都是地址。
func TestRedactStripsCertificateHostname(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://api.secret.example/v1", Err: &tls.CertificateVerificationError{
		Err: x509.HostnameError{Host: "api.secret.example", Certificate: &x509.Certificate{DNSNames: []string{"other.example"}}},
	}}
	got := upstream.Redact(err).Error()
	if strings.Contains(got, "secret.example") || strings.Contains(got, "other.example") {
		t.Errorf("Redact 后仍带主机名：%q", got)
	}
	if !strings.HasPrefix(got, "tls: failed to verify certificate: x509:") {
		t.Errorf("该留得住是证书校验这一步：%q", got)
	}
}

// 认不出的错误原样透过：超时那支要保住 errors.Is，业务上别的类型也不该被改写。
func TestRedactPassesUnknownErrorsThrough(t *testing.T) {
	if got := upstream.Redact(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("DeadlineExceeded 被改写成了 %v", got)
	}
	plain := errors.New("something else")
	if got := upstream.Redact(plain); got != plain {
		t.Errorf("普通错误被改写成了 %v", got)
	}
	if got := upstream.Redact(nil); got != nil {
		t.Errorf("nil 应回 nil，得到 %v", got)
	}
}
