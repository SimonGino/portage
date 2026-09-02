package server_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// readDump 读采样目录里唯一一组文件，按后缀返回。
func readDump(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("采样目录读不到（应当自动建）: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// 名字形如 <时间>-<序号>-<端点>.<后缀>。
		i := strings.Index(e.Name(), ".")
		if i < 0 {
			t.Fatalf("采样文件名没有后缀: %s", e.Name())
		}
		out[e.Name()[i+1:]] = string(raw)
	}
	return out
}

// PORTAGE_DUMP_DIR 采样（server/dump.go）：透传路径三份文件——入站原样、发上游的
// （只改了 model）、回客户端的（上游原样）。目录不预建，采样只认 env 不进配置。
func TestDumpCapturesPassthroughTriplet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dump", "nested")
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{DumpDir: dir})
	const upstreamBody = `{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"你好"}]}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Fatalf("开了采样之后客户端拿到的字节变了: %s", body)
	}

	got := readDump(t, dir)
	if got["in.json"] != anthropicRequest {
		t.Errorf("in.json 不是入站原样:\n%s", got["in.json"])
	}
	if got["out.json"] != forwardedRequest {
		t.Errorf("out.json 不是发给上游的那份:\n%s", got["out.json"])
	}
	if got["resp"] != upstreamBody {
		t.Errorf("resp 不是回给客户端的字节:\n%s", got["resp"])
	}
	for _, e := range mustReadDir(t, dir) {
		if !strings.Contains(e, "-v1_messages.") {
			t.Errorf("文件名没带端点: %s", e)
		}
	}
	if len(gw.Lines("排障采样已开启：每次转发的请求体、上游请求体与响应字节都会全文落盘，用完请关")) != 1 {
		t.Errorf("开采样该在启动时 Warn 一次")
	}
}

// 转换路径：out.json 是重编成渠道协议的那份，resp 是我们编回入口协议的整段 SSE。
// 这正是它要采的东西——真实 harness 第二轮回带的请求体只能在这条路上拿到（#95）。
func TestDumpCapturesConvertedStream(t *testing.T) {
	dir := t.TempDir()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, ccUpstreamModel, openaiCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{DumpDir: dir})
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
		strings.Join(ccExecStreamFrames(t), ""))

	resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
	client := gatewaytest.ReadBody(t, resp)

	got := readDump(t, dir)
	if got["in.json"] != convertResponsesRequest {
		t.Errorf("in.json 不是入站原样")
	}
	if !strings.Contains(got["out.json"], `"messages":`) || strings.Contains(got["out.json"], accessPointModel) {
		t.Errorf("out.json 该是发给 CC 上游的那份（messages + 纳管模型名）:\n%s", got["out.json"])
	}
	if got["resp"] != client {
		t.Errorf("resp 与客户端收到的 SSE 不一致\n采样: %s\n客户端: %s", got["resp"], client)
	}
	if !strings.Contains(got["resp"], "response.completed") {
		t.Errorf("resp 没采到完整的流")
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
