// Package gatewaytest is the main-seam test harness: a real gin server over a
// real SQLite file, talking to a fake upstream.
//
// 这是 M0 约定的主接缝——网关自身的 HTTP 边界。渠道 base_url 本来就是 DB 数据，
// 所以指向假上游不需要在生产代码里开任何注入点。后续里程碑复用本包，不要另造一套。
package gatewaytest

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/ai-gateway/internal/config"
	"github.com/SimonGino/ai-gateway/internal/server"
	"github.com/SimonGino/ai-gateway/internal/store"
)

// client 不设整体 Timeout（长流会被掐断），只限响应头的等待时长：网关若把首帧连同
// 响应头一起缓冲住了，这里会在 5 秒内快速失败，而不是让测试挂到 go test -timeout。
var client = &http.Client{
	Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Second},
}

// Received is one request as the fake upstream saw it.
//
// Host 单独存：net/http 把它从 Header 提升到了 Request.Host，断言时别去 Header 里找。
type Received struct {
	Method string
	Path   string
	Host   string
	Header http.Header
	Body   []byte
}

// Upstream is a fake channel. Set Handler to control what it returns.
type Upstream struct {
	*httptest.Server
	Handler http.HandlerFunc

	mu       sync.Mutex
	received []Received
}

// NewUpstream starts a fake upstream that, by default, answers with a canned
// Anthropic-shaped message.
func NewUpstream(t *testing.T) *Upstream {
	t.Helper()
	u := &Upstream{}
	u.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.received = append(u.received, Received{
			Method: r.Method, Path: r.URL.Path, Host: r.Host, Header: r.Header.Clone(), Body: body,
		})
		u.mu.Unlock()
		u.Handler(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

// Last returns the most recent request the upstream received.
func (u *Upstream) Last(t *testing.T) Received {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.received) == 0 {
		t.Fatal("假上游没有收到任何请求")
	}
	return u.received[len(u.received)-1]
}

// Count returns how many requests the upstream received.
func (u *Upstream) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.received)
}

// RespondWith makes the upstream answer with a fixed status, content type and body.
func (u *Upstream) RespondWith(status int, header map[string]string, body string) {
	u.Handler = func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// Gateway is a running gateway backed by a temporary database.
type Gateway struct {
	*httptest.Server
	DB  *sql.DB
	log *logCapture
}

// LogLine is one slog record the gateway emitted, with its attributes flattened.
type LogLine struct {
	Message string
	Attrs   map[string]any
}

// Str 取字符串属性；缺失即空串——断言「这个字段必须有值」时正好落空。
func (l LogLine) Str(key string) string {
	s, _ := l.Attrs[key].(string)
	return s
}

// Int64 取整数属性。slog 把 Go 的各种整型都归到 int64。
func (l LogLine) Int64(key string) int64 {
	switch v := l.Attrs[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

type logCapture struct {
	mu    sync.Mutex
	lines []LogLine
	raw   strings.Builder
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw.Write(p)
}

// captureHandler 两头都要：结构化的一份供逐字段断言，渲染后的一份供「整行里不该
// 出现凭证/base_url」这类扫描——泄漏可能发生在任何一个属性上，只查已知字段会漏。
type captureHandler struct {
	c    *logCapture
	text slog.Handler
	// preset 是通过 log.With(...) 预置的属性。必须真的记下来：直接丢掉的话，
	// 生产代码哪天改用 With 预置字段，这里的逐字段断言就会集体扑空——测试全绿，
	// 字段却已经不在日志里了。
	preset []slog.Attr
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	line := LogLine{Message: r.Message, Attrs: map[string]any{}}
	for _, a := range h.preset {
		line.Attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		line.Attrs[a.Key] = a.Value.Any()
		return true
	})
	h.c.mu.Lock()
	h.c.lines = append(h.c.lines, line)
	h.c.mu.Unlock()
	return h.text.Handle(ctx, r)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{
		c:      h.c,
		text:   h.text.WithAttrs(attrs),
		preset: append(append([]slog.Attr{}, h.preset...), attrs...),
	}
}

// WithGroup 直接炸：本捕获器没实现分组的键名前缀语义，静默接受只会让断言查不到
// 已经改了名的字段。生产代码真要用分组时，这里会立刻失败提醒补实现。
func (h *captureHandler) WithGroup(name string) slog.Handler {
	panic("gatewaytest 的日志捕获器尚不支持 slog 分组（" + name + "）：请先补实现再用")
}

// Lines returns every log record whose message is msg.
func (g *Gateway) Lines(msg string) []LogLine {
	g.log.mu.Lock()
	defer g.log.mu.Unlock()
	var out []LogLine
	for _, l := range g.log.lines {
		if l.Message == msg {
			out = append(out, l)
		}
	}
	return out
}

// LastCall returns the调用日志 line of the most recent relayed call.
func (g *Gateway) LastCall(t *testing.T) LogLine {
	t.Helper()
	lines := g.Lines("call")
	if len(lines) == 0 {
		t.Fatalf("没有落任何调用日志；已落的日志: %s", g.RawLog())
	}
	return lines[len(lines)-1]
}

// RawLog returns every log line as rendered text.
func (g *Gateway) RawLog() string {
	g.log.mu.Lock()
	defer g.log.mu.Unlock()
	return g.log.raw.String()
}

// Options overrides the startup configuration for the few tests that need it.
type Options struct {
	LogBodies bool
	// Retry 覆盖重试策略，零值即**关闭**重试——刻意不跟随 config.Default()。
	// M0 的一大批用例断言的是「上游回 429/500，网关原样透一次」，默认开着重试会
	// 让它们变成打三次、慢一秒，测的就不是原来那件事了；关掉才是 #13 要的那条
	// 「配 0 时行为与 M0 完全一致」的回归护栏。要测重试的用例显式传策略，其中
	// 至少有一个传 config.Default().Retry，保证出厂默认值本身也被跑到。
	Retry config.Retry
}

// NewDB creates a temporary database with the real schema applied.
func NewDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Start runs Validate against db and then serves the gateway. It fails the test
// if the startup gate rejects the seeded configuration — use store.Validate
// directly when the rejection is what you are asserting.
func Start(t *testing.T, db *sql.DB) *Gateway {
	t.Helper()
	return StartWith(t, db, Options{})
}

// StartWith is Start with configuration overrides.
func StartWith(t *testing.T, db *sql.DB, opts Options) *Gateway {
	t.Helper()
	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("启动校验未通过: %v", err)
	}
	cfg := config.Default()
	cfg.LogBodies = opts.LogBodies
	cfg.Retry = opts.Retry
	capture := &logCapture{}
	log := slog.New(&captureHandler{c: capture, text: slog.NewTextHandler(capture, nil)})
	srv := httptest.NewServer(server.New(cfg, db, log).Engine())
	t.Cleanup(srv.Close)
	return &Gateway{Server: srv, DB: db, log: capture}
}

// SeedPassthrough wires the smallest complete configuration: one channel with
// one credential and one managed model, exposed by one access point with one
// candidate.
func SeedPassthrough(t *testing.T, db *sql.DB, accessPointModel, proto, baseURL, upstreamModel, credential string) {
	t.Helper()
	channelID := SeedChannel(t, db, "test-"+proto, proto, baseURL, credential)
	modelID := SeedChannelModel(t, db, channelID, upstreamModel)
	apID := SeedAccessPoint(t, db, accessPointModel)
	SeedCandidate(t, db, apID, modelID, 100)
}

func SeedChannel(t *testing.T, db *sql.DB, name, proto, baseURL, credential string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO channels (name, protocol, base_url) VALUES (?, ?, ?)`, name, proto, baseURL)
	if err != nil {
		t.Fatalf("种渠道失败: %v", err)
	}
	id, _ := res.LastInsertId()
	if credential != "" {
		SeedCredential(t, db, id, credential)
	}
	return id
}

func SeedCredential(t *testing.T, db *sql.DB, channelID int64, credential string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, credential) VALUES (?, ?)`, channelID, credential); err != nil {
		t.Fatalf("种凭证失败: %v", err)
	}
}

func SeedChannelModel(t *testing.T, db *sql.DB, channelID int64, upstreamModel string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO channel_models (channel_id, upstream_model) VALUES (?, ?)`, channelID, upstreamModel)
	if err != nil {
		t.Fatalf("种纳管模型失败: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func SeedAccessPoint(t *testing.T, db *sql.DB, model string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO access_points (model) VALUES (?)`, model)
	if err != nil {
		t.Fatalf("种接入点失败: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func SeedCandidate(t *testing.T, db *sql.DB, accessPointID, channelModelID int64, weight int) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (?, ?, ?)`,
		accessPointID, channelModelID, weight); err != nil {
		t.Fatalf("种候选失败: %v", err)
	}
}

// Post sends a request to the gateway and returns the response with its body
// still unread, so a streaming test can consume it frame by frame.
func (g *Gateway) Post(t *testing.T, path, body string, header map[string]string) *http.Response {
	t.Helper()
	return g.PostCtx(t, t.Context(), path, body, header)
}

// PostCtx is Post with a caller-owned context, for cancellation tests.
func (g *Gateway) PostCtx(t *testing.T, ctx context.Context, path, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败（响应头迟迟不来通常意味着网关缓冲了首帧）: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Get sends a GET to the gateway. The caller owns closing the body.
func (g *Gateway) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, g.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败（响应头迟迟不来通常意味着网关缓冲了首帧）: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// ReadSome reads whatever has arrived on r, failing the test if nothing shows up
// within timeout. 这是 SSE 缓冲回归的探针：网关若把帧攒起来不 flush，这里就会超时。
func ReadSome(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	type result struct {
		data string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 4096)
		// (0, nil) 是合法的 Read 返回，要接着读而不是当成「读到了空内容」。
		for {
			n, err := r.Read(buf)
			if n > 0 || err != nil {
				ch <- result{string(buf[:n]), err}
				return
			}
		}
	}()
	select {
	case got := <-ch:
		if got.data == "" && got.err != nil {
			t.Fatalf("读流失败: %v", got.err)
		}
		return got.data
	case <-time.After(timeout):
		t.Fatalf("%s 内没有任何字节到达客户端——帧被缓冲住了", timeout)
		return ""
	}
}

// ReadBody drains and returns a response body.
func ReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体失败: %v", err)
	}
	return string(b)
}
