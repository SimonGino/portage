package server_test

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"
)

// 1h 缓存写落流水（#198）：上游把写入拆成 5m + 1h 报在嵌套 cache_creation 容器里，
// 透传路径的 Tap 旁路读出明细、流水落 cache_write_1h_tokens 列、计价把 1h 部分按
// 1h 价拆开算。样本形状取 testdata/fixtures/anthropic-*-cache-write-1h（构造样本，
// 派生自真实转录、只改 usage 数字——真转录的 1h 恒 0，请求没带 ttl:"1h"）。
//
// 流式与非流式各验一遍：两帧的 usage 形状不同（start 带容器、delta 不带），漏一边
// 都会只在某条路上吃掉明细。
func TestCacheWrite1hLandsInCallLogs(t *testing.T) {
	seedPricedAnthropic := func(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
		t.Helper()
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
		// Claude 世面价：入 3 / 缓写 3.75 / 1h 缓写 6（= 2× input，厂商定价事实）。
		var modelID int64
		if err := db.QueryRow(`SELECT id FROM channel_models`).Scan(&modelID); err != nil {
			t.Fatal(err)
		}
		p3, p15, p375, p6 := 3.0, 15.0, 3.75, 6.0
		if err := store.UpdateChannelModel(context.Background(), db, modelID, store.ChannelModelPatch{
			Prices: &store.ChannelModelPrices{
				Input: &p3, Output: &p15, CacheWrite: &p375, CacheWrite1H: &p6,
			},
		}); err != nil {
			t.Fatalf("填价: %v", err)
		}
		return gatewaytest.Start(t, db), up
	}
	// usage 形状照 fixture：净 input 37、写入 7059（5m 2059 + 1h 5000）、出 90。
	const usage = `"usage":{"input_tokens":37,"cache_creation_input_tokens":7059,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":2059,"ephemeral_1h_input_tokens":5000},` +
		`"cache_read_input_tokens":0,"output_tokens":90}`
	const nonStreamBody = `{"id":"msg_1","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` + usage + `}`
	const streamBody = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":37,"cache_creation_input_tokens":7059,"cache_creation":{"ephemeral_5m_input_tokens":2059,"ephemeral_1h_input_tokens":5000},"cache_read_input_tokens":0,"output_tokens":2}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":90}}

event: message_stop
data: {"type":"message_stop"}

`
	// 期望的账：净 37 × 3 + 出 90 × 15 + 写入 5m 2059 × 3.75 + 1h 5000 × 6（÷ 1e6）。
	wantCost := (37*3 + 90*15 + 2059*3.75 + 5000*6) / 1e6

	for _, tc := range []struct {
		name   string
		stream bool
		body   string
	}{
		{"非流式", false, nonStreamBody},
		{"流式", true, streamBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := seedPricedAnthropic(t)
			if tc.stream {
				up.RespondWith(http.StatusOK,
					map[string]string{"Content-Type": "text/event-stream"}, tc.body)
			} else {
				up.RespondWith(http.StatusOK,
					map[string]string{"Content-Type": "application/json"}, tc.body)
			}
			req := anthropicRequest
			if tc.stream {
				req = strings.Replace(req, `"stream":false`, `"stream":true`, 1)
			}
			gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", req, nil))

			row := gw.LastCallRow(t)
			if !row.CacheWrite1hTokens.Valid || row.CacheWrite1hTokens.Int64 != 5000 {
				t.Errorf("cache_write_1h_tokens = %+v, 期望 5000", row.CacheWrite1hTokens)
			}
			if !row.CacheWriteTokens.Valid || row.CacheWriteTokens.Int64 != 7059 {
				t.Errorf("cache_write_tokens = %+v, 期望 7059（明细不改变总量）", row.CacheWriteTokens)
			}
			// 毛值归一（口径层 v0.71）：净值 37 加回写入 7059。
			if !row.InputTokens.Valid || row.InputTokens.Int64 != 7096 {
				t.Errorf("input_tokens = %+v, 期望毛值 7096", row.InputTokens)
			}
			var cost sql.NullFloat64
			if err := gw.DB.QueryRow(`SELECT cost FROM call_logs ORDER BY id DESC LIMIT 1`).Scan(&cost); err != nil {
				t.Fatal(err)
			}
			if !cost.Valid || math.Abs(cost.Float64-wantCost) > 1e-9 {
				t.Errorf("cost = %+v, 期望 %v（1h 部分按 6 计、其余写入按 3.75）", cost, wantCost)
			}
		})
	}
}

// 未设 1h 价（#198 口径）：1h 部分退回 5 分钟价计，不自动按 2× input 落库。
func TestCacheWrite1hWithout1hPriceCountsAt5mRate(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	var modelID int64
	if err := db.QueryRow(`SELECT id FROM channel_models`).Scan(&modelID); err != nil {
		t.Fatal(err)
	}
	p3, p375 := 3.0, 3.75
	if err := store.UpdateChannelModel(context.Background(), db, modelID, store.ChannelModelPatch{
		Prices: &store.ChannelModelPrices{Input: &p3, CacheWrite: &p375},
	}); err != nil {
		t.Fatalf("填价: %v", err)
	}
	gw := gatewaytest.Start(t, db)

	const body = `{"id":"msg_1","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":37,"cache_creation_input_tokens":7059,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":2059,"ephemeral_1h_input_tokens":5000},` +
		`"cache_read_input_tokens":0,"output_tokens":0}}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, body)
	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	var cost sql.NullFloat64
	if err := gw.DB.QueryRow(`SELECT cost FROM call_logs ORDER BY id DESC LIMIT 1`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	want := (37*3 + 7059*3.75) / 1e6 // 整笔写入按 3.75，1h 不加价
	if !cost.Valid || math.Abs(cost.Float64-want) > 1e-9 {
		t.Errorf("cost = %+v, 期望 %v", cost, want)
	}
}
