// Package gatewaytest is the main-seam test harness: a real gin server over a
// real SQLite file, talking to a fake upstream.
//
// 这是 M0 约定的主接缝——网关自身的 HTTP 边界。渠道 base_url 本来就是 DB 数据，
// 所以指向假上游不需要在生产代码里开任何注入点。后续里程碑复用本包，不要另造一套。
package gatewaytest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/admin"
	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/server"
	"github.com/SimonGino/portage/internal/store"
)

// client 不设整体 Timeout（长流会被掐断），只限响应头的等待时长：网关若把首帧连同
// 响应头一起缓冲住了，这里会在 5 秒内快速失败，而不是让测试挂到 go test -timeout。
var client = &http.Client{
	Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Second},
}

// AnthropicResponseHeaders 是 Anthropic 官方文档列出的、这一跳必须原样回给客户端
// 的响应头（2026-08-13 核对 platform.claude.com/docs/en/api/rate-limits
// §Response headers 与 .../api/errors §Request ID）。
//
// 写死整套而不是「随便挑两个」，是因为 #37 的原始症状就是**中转把它们吃了**：
// 网关这边只要有一处退化成白名单转发，在中转下永远看不出区别，只有把整套头钉进
// 用例里才会当场红。Claude Code 靠 anthropic-ratelimit-* 退避，靠 request-id 报障。
//
// 值取文档里的样例形状（req_ 前缀、RFC 3339 的 reset），不是真实调用采来的——这一
// 层要验的是「网关有没有改动它们」，与值本身是不是真的无关。这**不构成** #37 那条
// 「官方直连实测」的验收：真实头名/大小写/有没有漏发，仍得拿官方 key 跑一次才算数。
var AnthropicResponseHeaders = map[string]string{
	"request-id":                                  "req_018EeWyXxfu5pfWkrYcMdjWG",
	"anthropic-organization-id":                   "org_01AbCdEfGhIjKlMnOpQrStUv",
	"anthropic-ratelimit-requests-limit":          "1000",
	"anthropic-ratelimit-requests-remaining":      "999",
	"anthropic-ratelimit-requests-reset":          "2026-08-13T04:05:06Z",
	"anthropic-ratelimit-tokens-limit":            "2400000",
	"anthropic-ratelimit-tokens-remaining":        "2399000",
	"anthropic-ratelimit-tokens-reset":            "2026-08-13T04:05:06Z",
	"anthropic-ratelimit-input-tokens-limit":      "2000000",
	"anthropic-ratelimit-input-tokens-remaining":  "1999000",
	"anthropic-ratelimit-input-tokens-reset":      "2026-08-13T04:05:06Z",
	"anthropic-ratelimit-output-tokens-limit":     "400000",
	"anthropic-ratelimit-output-tokens-remaining": "399000",
	"anthropic-ratelimit-output-tokens-reset":     "2026-08-13T04:05:06Z",
}

// Received is one request as the fake upstream saw it.
//
// Host 单独存：net/http 把它从 Header 提升到了 Request.Host，断言时别去 Header 里找。
type Received struct {
	Method   string
	Path     string
	RawQuery string
	Host     string
	Header   http.Header
	Body     []byte
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
			Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Host: r.Host, Header: r.Header.Clone(), Body: body,
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
//
// 要等，不能直接读：调用日志是 handler 的 defer 落的（server.go），而客户端的
// Post 在响应头一到就返回了——那时 handler 还没走完。本机上日志总是先落一步，
// CI 的慢机器上就不一定，TestCallLogRecordsRetryCount 在 GitHub Actions 上正是
// 这么红的。
func (g *Gateway) LastCall(t *testing.T) LogLine {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if lines := g.Lines("call"); len(lines) > 0 {
			return lines[len(lines)-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内没有落下调用日志；已落的日志: %s", g.RawLog())
			return LogLine{}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CallRow is one call_logs row, with the nullable columns kept nullable —
// 「没有」和「是 0」要能断言得出区别。
type CallRow struct {
	APIKeyName       string
	ClientProtocol   string
	UpstreamProtocol string
	ModelRequested   string
	ModelUpstream    string
	ChannelName      string
	ChannelKeyName   string
	Status           int
	RetryCount       int
	IsStream         sql.NullBool
	TTFTMs           sql.NullInt64
	TotalMs          int64
	QueueWaitMs      int64
	InputTokens      sql.NullInt64
	OutputTokens     sql.NullInt64
	CacheReadTokens  sql.NullInt64
	CacheWriteTokens sql.NullInt64
	// ReasoningTokens 是思考 token（口径层 v0.66）。可空：NULL 是「上游不报这个数」，
	// 0 是「上游报了，这次没思考」。
	ReasoningTokens sql.NullInt64
	Error           sql.NullString
	// ErrorDetail 是上游错误原文（口径层 v0.53）。可空——「没存过」与「存了空串」
	// 要分得开：后者是上游回了 4xx 但响应体是空的。
	ErrorDetail sql.NullString
	// UpstreamRequestID 是上游 request-id 的快照（口径层 v0.56）。不可空，没有即空串。
	UpstreamRequestID string
}

// LastCallRow returns the most recent call_logs row, waiting for it to land.
//
// 要等，理由同 LastCall：落库发生在 handler 的 defer 里，而客户端的 Post 在响应头
// 一到就返回了。而且落库排在 slog 之后——只等日志再直接读表，慢机器上必然偶发红。
func (g *Gateway) LastCallRow(t *testing.T) CallRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var r CallRow
		err := g.DB.QueryRow(`
			SELECT api_key_name, client_protocol, upstream_protocol,
			       model_requested, model_upstream, channel_name, channel_key_name,
			       status, retry_count, is_stream, ttft_ms, total_ms, queue_wait_ms,
			       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			       reasoning_tokens, error, error_detail,
			       upstream_request_id
			FROM call_logs ORDER BY id DESC LIMIT 1`).
			Scan(&r.APIKeyName, &r.ClientProtocol, &r.UpstreamProtocol,
				&r.ModelRequested, &r.ModelUpstream, &r.ChannelName, &r.ChannelKeyName,
				&r.Status, &r.RetryCount, &r.IsStream, &r.TTFTMs, &r.TotalMs, &r.QueueWaitMs,
				&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
				&r.ReasoningTokens, &r.Error, &r.ErrorDetail,
				&r.UpstreamRequestID)
		if err == nil {
			return r
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("读 call_logs 失败: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内 call_logs 没有落下任何行；已落的日志: %s", g.RawLog())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CountCallRows returns how many rows call_logs holds.
func (g *Gateway) CountCallRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := g.DB.QueryRow(`SELECT COUNT(*) FROM call_logs`).Scan(&n); err != nil {
		t.Fatalf("数 call_logs 失败: %v", err)
	}
	return n
}

// WaitCallRows 等 call_logs 攒够 want 行，再回最终行数。
//
// 为什么不能直接数：落库在 callLog 的 defer 里，也就是**响应已经发给客户端之后**
// （internal/server/auth.go）。这是有意的——不该为了写一行日志给每个请求加延迟。
// 代价是 Post 返回不代表那一行已经进库，直接数会间歇性少一行（CI 上实见，本地
// `-count=60` 也能复现）。LastCallRow 挡不住这个：它只等「有任意一行」。
//
// 数够之后还静置一下再回，是为了不放过「一次请求写两行」——只等「≥ want」的话，
// 前一个请求重复落库、后一个还没落时会凑巧数出 want，而那正是调用方要防的 bug。
func (g *Gateway) WaitCallRows(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for g.CountCallRows(t) < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	return g.CountCallRows(t)
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
	// RateLimitQPS / RateLimitBurst 覆盖全局限流，零值即**关闭**——同样刻意不跟随
	// config.Default()。默认的 10 QPS / 突发 20 会让任何连打二十几个请求的用例莫名
	// 变红，而且是间歇性的（跑得慢的机器上令牌来得及回）。要测限流的用例显式传值。
	RateLimitQPS   int
	RateLimitBurst int
	// Queue 覆盖并发闸排队参数。与上面两个相反，零值**跟随** config.Default()：
	// 排队参数只对设了 max_concurrency 的渠道生效，默认渠道不设上限，跟随默认值
	// 不会改变任何既有用例的行为；而 Wait 停在 0 会让闸上的每次排队立即超时——
	// 那不是「关闭」，是一个没人想要的形态。
	Queue config.Queue
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

// DefaultKey 是 Start 自动种下、Post/Get 自动附带的网关 key。
//
// 有了它，M1 鉴权接进来之后既有用例一行都不用改，而且走的是**真实**鉴权路径而非
// 绕过——比给测试开后门强。要测「不带 key」「带错 key」的用例显式传头覆盖，传空串
// 即「不出示」。
const DefaultKey = "sk-ptg-test-default"

// StartWith is Start with configuration overrides.
func StartWith(t *testing.T, db *sql.DB, opts Options) *Gateway {
	t.Helper()
	SeedAPIKey(t, db, "test-default", DefaultKey)
	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("启动校验未通过: %v", err)
	}
	cfg := config.Default()
	cfg.LogBodies = opts.LogBodies
	cfg.Retry = opts.Retry
	cfg.RateLimitQPS = opts.RateLimitQPS
	cfg.RateLimitBurst = opts.RateLimitBurst
	if opts.Queue != (config.Queue{}) {
		cfg.Queue = opts.Queue
	}
	cfg.AdminPassword = AdminPassword
	// 走真正的 Bootstrap，不直接往 settings 里塞哈希：管理端测试要覆盖的正是
	// 「配置里的明文只用来初始化」这条口径，绕过它就等于没测。
	if _, err := admin.Bootstrap(t.Context(), db, cfg.AdminPassword); err != nil {
		t.Fatalf("初始化管理端密码失败: %v", err)
	}
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

// SeedAPIKey 种一把网关 key。重复种同一把不算错——Start 每次都会种 DefaultKey，
// 而同一个库可能被起两次。
func SeedAPIKey(t *testing.T, db *sql.DB, name, plain string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO api_keys (name, key_hash) VALUES (?, ?)`, name, auth.Hash(plain)); err != nil {
		t.Fatalf("种网关 key 失败: %v", err)
	}
}

// SeedChannel 种一个渠道。protocols 是支持协议集（口径层 v0.33），逗号分隔——
// 传单个协议名就是一元集合，绝大多数用例都这么用。
func SeedChannel(t *testing.T, db *sql.DB, name, protocols, baseURL, credential string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO channels (name, protocols, base_url) VALUES (?, ?, ?)`, name, protocols, baseURL)
	if err != nil {
		t.Fatalf("种渠道失败: %v", err)
	}
	id, _ := res.LastInsertId()
	if credential != "" {
		SeedCredential(t, db, id, credential)
	}
	return id
}

// SetChannelConcurrency 给渠道设并发上限（口径层 v0.49），0 = 不限。
func SetChannelConcurrency(t *testing.T, db *sql.DB, channelID int64, limit int) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE channels SET max_concurrency = ? WHERE id = ?`, limit, channelID); err != nil {
		t.Fatalf("设渠道并发上限失败: %v", err)
	}
}

// SetChannelCompaction 设渠道的 compaction 能力位（口径层 v0.54）。库里的默认是
// false，所以只有「上游确实认得 compaction_trigger」的用例需要调它。
func SetChannelCompaction(t *testing.T, db *sql.DB, channelID int64, supports bool) {
	t.Helper()
	v := 0
	if supports {
		v = 1
	}
	if _, err := db.Exec(
		`UPDATE channels SET supports_compaction = ? WHERE id = ?`, v, channelID); err != nil {
		t.Fatalf("设渠道 compaction 能力位失败: %v", err)
	}
}

// SeedCredential 往渠道的凭证池里追加一份，名字自动给 `凭证 N`（渠道内唯一，
// 口径层 v0.38）。要指定名字（按凭证归因的用例要）用 SeedNamedCredential。
func SeedCredential(t *testing.T, db *sql.DB, channelID int64, credential string) {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM channel_keys WHERE channel_id = ?`, channelID).Scan(&n); err != nil {
		t.Fatalf("数凭证失败: %v", err)
	}
	SeedNamedCredential(t, db, channelID, fmt.Sprintf("凭证 %d", n+1), credential)
}

// SeedNamedCredential 种一份带名字的凭证，返回它的 id——摘除类用例要用 id 去核对
// disabled_reason / disabled_at。
func SeedNamedCredential(t *testing.T, db *sql.DB, channelID int64, name, credential string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (?, ?, ?)`,
		channelID, name, credential)
	if err != nil {
		t.Fatalf("种凭证失败: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
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
	attachDefaultKey(req, header)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败（响应头迟迟不来通常意味着网关缓冲了首帧）: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// attachDefaultKey 补上 DefaultKey，除非调用方自己出示了凭证头。
//
// 判断看的是**键在不在** header 里，不是值非不非空：传空串正是「不出示任何凭证」
// 这个用例的写法，此时补一把有效 key 就把它测没了。
func attachDefaultKey(req *http.Request, header map[string]string) {
	for k := range header {
		if strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "Authorization") {
			return
		}
	}
	req.Header.Set("x-api-key", DefaultKey)
}

// Get sends a GET to the gateway. The caller owns closing the body.
func (g *Gateway) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	return g.GetWith(t, path, nil)
}

// GetWith is Get with caller-supplied headers, for the rejection cases.
func (g *Gateway) GetWith(t *testing.T, path string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, g.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	attachDefaultKey(req, header)
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
