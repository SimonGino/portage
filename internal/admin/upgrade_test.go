package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/selfupdate"
	"github.com/SimonGino/portage/internal/store"
)

// 声明文件形态照常可用（不进写闸）；成功回 200 并通知收场；之后再来一次是 already_running。
func TestUpgradeSucceedsOnceAndNotifies(t *testing.T) {
	got := make(chan string, 1)
	srv, _, _ := newAuthServer(t, true, func(h *Handler) {
		h.apply = func(context.Context, string) error { return nil }
		h.onUpgraded = func(v string) { got <- v }
	})
	cl := newClient(t, srv)
	cl.login(t, store.FirstAdminEmail, testAdminPassword)

	if status, body, _ := cl.do(t, http.MethodPost, "/panel/api/upgrade", `{"version":"v0.1.1"}`); status != http.StatusOK {
		t.Fatalf("upgrade = %d %s，期望 200", status, body)
	}
	if v := <-got; v != "0.1.1" {
		t.Fatalf("onUpgraded 收到 %q，期望 0.1.1", v)
	}
	if status, body, _ := cl.do(t, http.MethodPost, "/panel/api/upgrade", `{"version":"0.1.1"}`); status != http.StatusConflict || body != `{"error":"already_running"}` {
		t.Fatalf("第二次 upgrade = %d %s，期望 409 already_running", status, body)
	}
}

// 失败按词表回，并复位闸门：修好网络再点一次得能进。
func TestUpgradeFailureWordsAndRetry(t *testing.T) {
	var err error
	srv, _, _ := newAuthServer(t, false, func(h *Handler) {
		h.apply = func(context.Context, string) error { return err }
	})
	cl := newClient(t, srv)
	cl.login(t, store.FirstAdminEmail, testAdminPassword)

	for _, c := range []struct {
		err    error
		status int
		body   string
	}{
		{selfupdate.ErrChecksum, http.StatusBadGateway, `{"error":"checksum_mismatch"}`},
		{selfupdate.ErrNotWritable, http.StatusConflict, `{"error":"not_writable"}`},
		{selfupdate.ErrDownload, http.StatusBadGateway, `{"error":"download_failed"}`},
	} {
		err = c.err
		if status, body, _ := cl.do(t, http.MethodPost, "/panel/api/upgrade", `{"version":"0.1.1"}`); status != c.status || body != c.body {
			t.Errorf("%v → %d %s，期望 %d %s", c.err, status, body, c.status, c.body)
		}
	}
	if status, _, _ := cl.do(t, http.MethodPost, "/panel/api/upgrade", `{"version":"../../x"}`); status != http.StatusBadRequest {
		t.Errorf("非 x.y.z 版本号应 400，得到 %d", status)
	}
}

func TestUpgradeRejectsDocker(t *testing.T) {
	srv, _, _ := newAuthServer(t, false, func(h *Handler) {
		h.distro = "docker"
		h.apply = func(context.Context, string) error { t.Error("docker 形态不该走到替换"); return nil }
	})
	cl := newClient(t, srv)
	cl.login(t, store.FirstAdminEmail, testAdminPassword)
	if status, body, _ := cl.do(t, http.MethodPost, "/panel/api/upgrade", `{"version":"0.1.1"}`); status != http.StatusBadRequest || body != `{"error":"unsupported_distro"}` {
		t.Fatalf("docker upgrade = %d %s，期望 400 unsupported_distro", status, body)
	}
}
