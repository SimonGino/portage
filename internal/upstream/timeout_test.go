package upstream_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/upstream"
)

// TestTimeoutRecognizesRealTransportTimeout 用一条真的超时错误钉判据，不用手搓的
// 假 net.Error：Timeout() 的价值全在「Go 把超时裹了几层之后还认不认得出」，
// *url.Error 那层壳正是要跨过去的东西（口径层 v1.16 的 502/504 分档全靠它）。
func TestTimeoutRecognizesRealTransportTimeout(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("期望超时，实际拿到了响应")
	}
	if !upstream.Timeout(err) {
		t.Errorf("Timeout(%v) = false, 期望 true", err)
	}
}

// 链路坏掉与超时是两件事：连不上、证书错、EOF 都不该被判成「等超时了」，
// 否则 504 会把「这个地址根本连不上」也罩进去。
func TestTimeoutRejectsNonTimeoutErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close() // 端口随即空出，连接被拒

	if _, err := http.Get(dead); err == nil {
		t.Fatal("期望连不上，实际连上了")
	} else if upstream.Timeout(err) {
		t.Errorf("Timeout(%v) = true, 连接被拒不是超时", err)
	}
	if upstream.Timeout(errors.New("上游回了一堆看不懂的字节")) {
		t.Error("普通错误被判成了超时")
	}
}
