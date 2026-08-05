package server

import (
	"time"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// bodyCaptureLimit 是 log_bodies 打开时单侧 body 的记录上限。开这个开关是为了排障，
// 不是为了留档；一条长流的完整响应进日志只会把日志冲垮。
const bodyCaptureLimit = 64 << 10

// captureWriter 是 log_bodies 打开时挂在旁路上的定量收集器。和 Tap 一样：永不报错，
// 否则 io.MultiWriter 会把错误变成读错误、打断转发。
type captureWriter struct {
	buf       []byte
	truncated bool
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if room := bodyCaptureLimit - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *captureWriter) String() string {
	if c.truncated {
		return string(c.buf) + "…[truncated]"
	}
	return string(c.buf)
}

// callRecord 攒够一次调用的日志字段，由 relay 的 defer 统一落一行。
//
// 里面**没有**渠道 base_url 与凭证，也不该被加进来：日志是最容易被复制粘贴出去的
// 东西，上游密钥不进日志是硬约束。渠道用 name 指代已经够定位。
type callRecord struct {
	start     time.Time
	firstByte time.Time

	endpoint      string
	inboundProto  protocol.Protocol
	accessPoint   string
	channel       string
	channelProto  protocol.Protocol
	upstreamModel string
	stream        bool
	status        int
	// outcome 区分「同样是 200」的几种收场，尤其是首字节之后断流那种——
	// 状态码已经发出去了，只有这里能看出它其实没说完。
	outcome string

	summary     protocol.Summary
	haveSummary bool

	requestBody  *captureWriter
	responseBody *captureWriter
}

func (s *Server) logCall(rec *callRecord) {
	attrs := []any{
		"endpoint", rec.endpoint,
		"inbound_protocol", string(rec.inboundProto),
		"access_point", rec.accessPoint,
		"stream", rec.stream,
		"status", rec.status,
		"outcome", rec.outcome,
		"duration_ms", time.Since(rec.start).Milliseconds(),
	}
	if rec.channel != "" {
		attrs = append(attrs, "channel", rec.channel,
			"channel_protocol", string(rec.channelProto),
			"upstream_model", rec.upstreamModel)
	}
	if !rec.firstByte.IsZero() {
		attrs = append(attrs, "ttfb_ms", rec.firstByte.Sub(rec.start).Milliseconds())
	}
	if rec.haveSummary {
		sum := rec.summary
		attrs = append(attrs,
			"input_tokens", sum.InputTokens,
			"output_tokens", sum.OutputTokens,
			"cache_read_tokens", sum.CacheReadTokens,
			"cache_write_tokens", sum.CacheWriteTokens,
			"stop_reason", sum.StopReason,
			// upstream_reported_model 是上游自报的模型名，和 upstream_model
			// 对不上时说明上游把别名解析成了别的版本。
			"upstream_reported_model", sum.Model,
		)
		if sum.Degraded {
			attrs = append(attrs, "tap_degraded", true)
		}
	}
	if rec.requestBody != nil {
		attrs = append(attrs, "request_body", rec.requestBody.String())
	}
	if rec.responseBody != nil {
		attrs = append(attrs, "response_body", rec.responseBody.String())
	}
	s.log.Info("call", attrs...)
}
