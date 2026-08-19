package server_test

import (
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件是 #11 收编 internal/calllog 之前先补的安全网（C0）。它钉的全是**库里那一行**
// 的形状：哪几列有值、哪几列留 NULL、词表落的是哪个词。收编前后这些断言一个字都
// 不该改——改了就说明重构改了行为，而不是用例过时。
//
// 与 calllog_test.go 的分工：那边断的是 slog 那一行，这边断的是 call_logs 那一行。
// 同一件事在两侧的表达不同（slog 恒打字段、库里留 NULL），只断一侧漏得掉另一侧。

// ── ① token 三列的库侧断言，分协议各一条 ──────────────────────────────────
//
// input_tokens / cache_read_tokens / cache_write_tokens 此前只在 slog 侧被断过，
// 库侧零断言。**分协议是硬要求**：#6 要把 Anthropic 的净值 input 归一成毛值，
// 而 CC 的 input 本来就是毛值、一个字都不该动——只有两条并排放着，才分得出归一
// 有没有误伤到不该动的那一侧。

// Anthropic 渠道：上游报的 input_tokens 是**净值**（与缓存两项互不相交），落流水
// 前经 protocol.GrossSummaryInput 归一成毛值（#6，口径层 v0.71）。
//
// 2091 = 31 + 12 + 2048。缓存两项照旧原样落列，明细不因归一而丢。
func TestAnthropicTokenColumnsLandInCallLogs(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",`+
			`"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":31,"cache_creation_input_tokens":12,`+
			`"cache_read_input_tokens":2048,"output_tokens":57}}`)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	row := gw.LastCallRow(t)
	assertNullInt(t, "input_tokens", row.InputTokens, 2091)
	assertNullInt(t, "output_tokens", row.OutputTokens, 57)
	assertNullInt(t, "cache_read_tokens", row.CacheReadTokens, 2048)
	assertNullInt(t, "cache_write_tokens", row.CacheWriteTokens, 12)
}

// CC 渠道：input_tokens 本来就是**毛值**（prompt_tokens 已含缓存命中那部分）。
//
// 2079 = 31 + 2048，与上面那条 Anthropic 是同一次调用的两种记法（CC 没有缓存写
// 一项，所以差 12）。#6 归一只该动 Anthropic 那条——这条的 2079 从头到尾不动，
// 动了即归一误伤。
//
// cache_write_tokens 落 0 而不是 NULL：CC 协议没有缓存写入的概念，上游一定不报，
// 但只要走到了 summary 这一列就恒落（口径层 v0.66 只给思考那一格开了「没报就
// NULL」的例外）。
func TestChatCompletionsTokenColumnsLandInCallLogs(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-cc", "openai", "qwen3-max")
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-1","object":"chat.completion","model":"qwen3-max",`+
			`"choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":2079,"completion_tokens":57,`+
			`"prompt_tokens_details":{"cached_tokens":2048}}}`)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/chat/completions", ccRequest, nil))

	row := gw.LastCallRow(t)
	assertNullInt(t, "input_tokens", row.InputTokens, 2079)
	assertNullInt(t, "output_tokens", row.OutputTokens, 57)
	assertNullInt(t, "cache_read_tokens", row.CacheReadTokens, 2048)
	assertNullInt(t, "cache_write_tokens", row.CacheWriteTokens, 0)
}

// ── ② 词表里此前只在 slog 侧断过的那四个词 ──────────────────────────────
//
// 10 个词（CONTEXT.md「outcome 词表」）里，upstream_error / unauthorized /
// queue_full / queue_timeout / queue_abandoned / compaction_unsupported 已经有库侧
// 断言；下面这四条补齐剩下的四个。词落错一格库里就是另一个字符串，而读者
// （group by error）没有第二个信源。

// stream_aborted：状态码早已是 200 发出去了，只有 error 列能看出这次没说完。
func TestStreamAbortedLandsInErrorColumn(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{})
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseFrame("message_start",
			`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5}}}`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	_, _ = io.ReadAll(resp.Body)

	row := gw.LastCallRow(t)
	if row.Error.String != "stream_aborted" {
		t.Errorf("error = %q, 期望 stream_aborted", row.Error.String)
	}
	if row.Status != http.StatusOK {
		t.Errorf("status = %d；断流发生在 200 已发出之后", row.Status)
	}
}

// rejected：构造时的默认词，走到这里说明没有任何分支覆盖过它。count_tokens 501
// 那个老载体在 #18 之后改判本地估算回 200，这里换 previous_response_id 撞转换
// 路径那条 400 当载体（口径层 v0.88，同为早退分支的缺省词）。
func TestRejectedLandsInErrorColumn(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "qwen3-max", openaiCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/responses", statefulRequest, nil))

	row := gw.LastCallRow(t)
	if row.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, 期望 400", row.Status)
	}
	if row.Error.String != "rejected" {
		t.Errorf("error = %q, 期望 rejected", row.Error.String)
	}
}

// model_not_allowed：白名单挡下的那一档。这条路径本来就被 TestAllowedModelsIsEnforced
// 走到了，只是没人断过 error 列。
func TestModelNotAllowedLandsInErrorColumn(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	const limited = "sk-ptg-only-other-point"
	if _, err := gw.DB.Exec(
		`INSERT INTO api_keys (name, key_hash, allowed_models) VALUES (?, ?, ?)`,
		"限别的接入点", auth.Hash(limited), "some-other-point"); err != nil {
		t.Fatal(err)
	}

	resp := gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": limited})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, 期望 403", resp.StatusCode)
	}
	if up.Count() != 0 {
		t.Error("被白名单挡下的请求打到上游去了")
	}

	row := gw.LastCallRow(t)
	if row.Error.String != "model_not_allowed" {
		t.Errorf("error = %q, 期望 model_not_allowed", row.Error.String)
	}
}

// rate_limited：限流挡下的那一档。同 TestRateLimitedCallLandsInCallLogs 的取行纪律
// ——必须先 WaitCallRows(2) 再读最后一行，否则读到的是第一个请求那条 200。
func TestRateLimitedLandsInErrorColumn(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("第 2 个请求 = %d, 期望 429", resp.StatusCode)
	}
	if n := gw.WaitCallRows(t, 2); n != 2 {
		t.Fatalf("call_logs 落了 %d 行，期望 2", n)
	}

	row := gw.LastCallRow(t)
	if row.Error.String != "rate_limited" {
		t.Errorf("error = %q, 期望 rate_limited", row.Error.String)
	}
}

// ── ③ 三段可空映射的边界 ────────────────────────────────────────────────
//
// 「有没有解析到请求」「有没有来过首字节」「有没有 summary」这三段判据今天分别
// 借 requestedModel != ""、firstByte.IsZero()、一个裸 bool 位。前两个换掉判据、把
// 条件简化掉，现有用例一条都不会红——下面这三组就是补上的那张网。

// is_stream 的三态边界。判据是「有没有解析出 model」，与「这次成功没成功」正交：
// 501 / 403 都是被拒的行，但它们**解析过请求体**，那一格照样有值。
func TestIsStreamNullBoundary(t *testing.T) {
	t.Run("鉴权失败/没读过请求体", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})
		if row := gw.LastCallRow(t); row.IsStream.Valid {
			t.Errorf("is_stream = %v, 期望 NULL", row.IsStream.Bool)
		}
	})

	t.Run("请求体不是合法 JSON/400", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		resp := gw.Post(t, "/v1/messages", `{"model":"gw-sonnet",`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, 期望 400", resp.StatusCode)
		}
		if row := gw.LastCallRow(t); row.IsStream.Valid {
			t.Errorf("is_stream = %v, 解不动的请求体不知道是不是流式，期望 NULL", row.IsStream.Bool)
		}
	})

	t.Run("缺 model 字段/400", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		resp := gw.Post(t, "/v1/messages", `{"stream":true,"messages":[]}`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, 期望 400", resp.StatusCode)
		}
		// 请求体里明明写了 stream:true，这一格仍要留 NULL：判据是「解析到请求」这件
		// 整事，缺 model 的请求在 400 那一行就返回了，从没走到赋值那一步。
		if row := gw.LastCallRow(t); row.IsStream.Valid {
			t.Errorf("is_stream = %v, 缺 model 的请求没走到赋值那一步，期望 NULL", row.IsStream.Bool)
		}
	})

	t.Run("限流/429", func(t *testing.T) {
		gw, _ := newLimitedGateway(t, 1)
		gw.Post(t, "/v1/messages", anthropicRequest, nil)
		gw.Post(t, "/v1/messages", anthropicRequest, nil)
		if n := gw.WaitCallRows(t, 2); n != 2 {
			t.Fatalf("call_logs 落了 %d 行，期望 2", n)
		}
		// 限流挂在鉴权之后、relay 之前，同样没读过请求体。
		if row := gw.LastCallRow(t); row.IsStream.Valid {
			t.Errorf("is_stream = %v, 期望 NULL", row.IsStream.Bool)
		}
	})

	t.Run("501 被拒但解析过请求体/有值", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
		gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})

		gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil))

		// 反方向的边界：outcome=rejected 不代表「不知道」。转换闸拦在解析请求体
		// **之后**，这一行确凿地知道客户端要的是非流式。
		row := gw.LastCallRow(t)
		if !row.IsStream.Valid || row.IsStream.Bool {
			t.Errorf("is_stream = %+v, 501 的行解析过请求体，期望 0（非流式）", row.IsStream)
		}
	})
}

// ttft_ms 的边界：流式且**真的来过字节**才落。声明了流式却一个字节都没到（上游回
// 空 body）时留 NULL——落一个 0 会在「平均首字延迟」里凭空多一个满分样本。
func TestTTFTNullWhenStreamNeverDelivered(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"}, "")

	resp := gw.Post(t, "/v1/messages",
		strings.Replace(anthropicRequest, `"stream":false`, `"stream":true`, 1), nil)
	gatewaytest.ReadBody(t, resp)

	row := gw.LastCallRow(t)
	if !row.IsStream.Valid || !row.IsStream.Bool {
		t.Fatalf("is_stream = %+v, 前提不成立：这条要的是一次流式请求", row.IsStream)
	}
	if row.TTFTMs.Valid {
		t.Errorf("ttft_ms = %d, 一个字节都没来的流该是 NULL", row.TTFTMs.Int64)
	}
}

// 没有 summary（压根没走到上游）→ token 五列全 NULL。落 0 是在替上游说「这次用了
// 0 个 token」，而我们其实什么都不知道。
func TestTokenColumnsAllNullWithoutSummary(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})

	row := gw.LastCallRow(t)
	for name, v := range map[string]sql.NullInt64{
		"input_tokens":       row.InputTokens,
		"output_tokens":      row.OutputTokens,
		"cache_read_tokens":  row.CacheReadTokens,
		"cache_write_tokens": row.CacheWriteTokens,
		"reasoning_tokens":   row.ReasoningTokens,
	} {
		if v.Valid {
			t.Errorf("%s = %d, 没走到上游的行该是 NULL", name, v.Int64)
		}
	}
}

func assertNullInt(t *testing.T, name string, got sql.NullInt64, want int64) {
	t.Helper()
	if !got.Valid {
		t.Errorf("%s 是 NULL, 期望 %d", name, want)
		return
	}
	if got.Int64 != want {
		t.Errorf("%s = %d, 期望 %d", name, got.Int64, want)
	}
}
