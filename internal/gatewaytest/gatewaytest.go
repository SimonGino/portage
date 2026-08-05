// Package gatewaytest is the main-seam test harness: a real gin server over a
// real SQLite file, talking to a fake upstream.
//
// 这是 M0 约定的主接缝——网关自身的 HTTP 边界。渠道 base_url 本来就是 DB 数据，
// 所以指向假上游不需要在生产代码里开任何注入点。后续里程碑复用本包，不要另造一套。
package gatewaytest

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/config"
	"github.com/SimonGino/ai-gateway/internal/server"
	"github.com/SimonGino/ai-gateway/internal/store"
)

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
	DB *sql.DB
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
	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("启动校验未通过: %v", err)
	}
	cfg := config.Default()
	log := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(server.New(cfg, db, log).Engine())
	t.Cleanup(srv.Close)
	return &Gateway{Server: srv, DB: db}
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

// Post sends a request to the gateway and returns the response. The caller owns
// closing the body.
func (g *Gateway) Post(t *testing.T, path, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求网关失败: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
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
