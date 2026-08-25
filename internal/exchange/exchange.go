// Package exchange 收「一次上游交换」的公共编排（#9）：dial → attempt 记账回填 →
// 队列闸/传输错误收场 → request-id 写回 → 观察者装配。透传与转换两条 relay 各以
// adapter 的身份坐在这道 seam 两侧，差别缩到参数上：出站端点、查询串带不带、错误
// 原文是旁路占坑还是读全在手（TapErrorBody）。#20 出站端点、v0.53 错误详情、
// v0.74 三级 request-id 这些不变式自此只在这里推一遍。
//
// 客户端侧的写盘纪律（写超时推进、首字节回调、逐块 flush）同样收在本包（Writer），
// 见 writer.go。
package exchange

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/taps"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"
)

// Client 是一次上游交换的编排方，由 server 在启动时装配一份、全部请求共用。
type Client struct {
	Up  *upstream.Client
	Log *slog.Logger
	// LogBodies 打开时给每次交换挂一路响应体旁路（rec.TapResponseBody）。
	LogBodies bool
	// QueueRetryAfter 是并发闸 429 的 Retry-After 值（整秒字符串），启动时换算一次。
	QueueRetryAfter string
}

// Request 是一次交换的全部输入。字段即两条 relay 的差异点：透传的 Endpoint 是入站
// 那条（入口即出口，#20）、RawQuery 整串照抄、TapErrorBody 为真；转换的 Endpoint
// 是渠道协议的上游端点、查询串不带（入口协议的方言照抄过去不是保真是串味）、
// 错误体由调用方读全在手（rec.UpstreamRejected），TapErrorBody 为假。
type Request struct {
	Rec *calllog.Recorder
	// Inbound 是入站协议：闸拒与传输错误都按它的原生形态回给客户端。
	Inbound  protocol.Protocol
	Cand     store.Candidate
	Endpoint protocol.Endpoint
	RawQuery string
	Body     []byte
	Header   http.Header
	Stream   bool
	// TapErrorBody 为真时，上游回 >= 400 就挂 rec.TapUpstreamErrorBody 旁路（口径层
	// v0.53）：透传路径上响应字节属于客户端，不能为了记一份错误体把它先攒进内存。
	// 转换路径不挂——那边非 2xx 的字节反正要读全（rec.UpstreamRejected），占了坑
	// 反而让「先到先得」把同一段原文记两半。
	TapErrorBody bool
}

// Result 是一次拿到响应头之后的交换现场。Body 是已挂好观察者的读端；收场统一走
// Close（挂进调用方的 defer），顺序在那里定死。
type Result struct {
	Status int
	Header http.Header
	// Body 是给调用方消费的读端：Tap、LogBodies 收集器、错误原文旁路都已经 tee 在
	// 上面，拿到的是与转发**同一份**字节，且都写不坏转发——它们的 Write 恒不报错，
	// io.MultiWriter 因此也不会。
	Body io.Reader

	resp *http.Response
	rec  *calllog.Recorder
	tap  protocol.Tap
}

// UpstreamBody 是上游响应体本尊（Body 是它包了 Tee 的读端）。写出失败的收场要
// 先关它再排空解码通道（#8，见 server 侧的 abortDecode）——那一步要的是「断上游」
// 这个动作本身，不能拿包过 Tee 的读端去关。
func (r *Result) UpstreamBody() io.Closer { return r.resp.Body }

// Close 收场：先交 usage（若挂了 Tap），再关上游 body。收成**一个** defer 而不是
// 两个，顺序（Summarize 先于 Close）在这里定死，调用方不可能注册反。
//
// panic 展开路径（首字节后写出失败）它同样兜住：透传路径的 Tap 写发生在调用方自己
// 的读循环里，展开时早已停止；转换路径由 abortDecode 先排空到通道关闭才 panic，
// Summary 读到的都是解码侧写完的最终状态（#8 的 happens-before 边）。
func (r *Result) Close() {
	if r.tap != nil {
		r.rec.Summarized(r.tap.Summary())
	}
	r.resp.Body.Close()
}

// Do 跑一次上游交换：记出站端点 → 发请求 → 回填 attempt 三数 → 错误则按入站协议
// 收场（已写回客户端，返回 nil, false）→ 成功则写回 request-id 候选并装配观察者。
//
// 出站端点在发请求之前的这一刻才记（#20）：调用方那边解不动请求体、编不出出口
// 请求、压缩闸拒绝那些早退都停在进本函数之前，它们没打上游，那一格该空着。
func (x *Client) Do(ctx context.Context, w http.ResponseWriter, req Request) (*Result, bool) {
	rec := req.Rec
	rec.Dialing(req.Endpoint.Path)
	resp, at, err := x.Up.Do(ctx, req.Cand, req.Endpoint, req.RawQuery, req.Body, req.Header, req.Stream)
	rec.Attempted(at.Retries(), at.Credential, at.QueueWait)
	if err != nil {
		if x.writeQueueReject(w, req, err) {
			return nil, false
		}
		// 只报渠道名；Redact 摘掉传输错误里内嵌的 base_url。
		// 这一支没有响应体可截，落库的原文就是这条传输错误本身（口径层 v0.53）。
		// 不落的话，最想看细节的那半边——连不上、握手失败、读超时——恰好永远是空。
		rec.Failed(calllog.UpstreamError, upstream.Redact(err).Error())
		x.Log.Error("上游请求失败", "channel", req.Cand.ChannelName, "err", upstream.Redact(err))
		req.Inbound.WriteError(w, http.StatusBadGateway, "上游渠道 "+req.Cand.ChannelName+" 请求失败")
		return nil, false
	}
	// 在写响应头之前取：之后客户端侧的 Header() 里也有同一个值，但从上游的
	// resp.Header 拿才是「上游报的」，不受本地补头（X-Accel-Buffering）干扰。
	// 只取两档头候选，最终取哪个由流水收尾时定——中间那档在错误体里（v0.74）。
	rec.RequestIDs(upstream.RequestIDs(resp.Header))

	res := &Result{Status: resp.StatusCode, Header: resp.Header, resp: resp, rec: rec}
	var observers []io.Writer
	if tap := taps.New(req.Cand.Protocol, req.Stream); tap != nil {
		observers = append(observers, tap)
		res.tap = tap
	}
	if x.LogBodies {
		observers = append(observers, rec.TapResponseBody())
	}
	// 上游说不行时，把它说的话截一段落库（口径层 v0.53）。挂旁路而不是先读后转，
	// 理由见 Request.TapErrorBody。判据是状态码而非 error 列——透传 4xx 的 error
	// 列是空的（v0.28 纪律）。
	if req.TapErrorBody && resp.StatusCode >= 400 {
		observers = append(observers, rec.TapUpstreamErrorBody())
	}
	res.Body = resp.Body
	if len(observers) > 0 {
		res.Body = io.TeeReader(resp.Body, io.MultiWriter(observers...))
	}
	return res, true
}

// writeQueueReject 译写渠道并发闸的三种收场（口径层 v0.50/v0.52）；不是闸的错误
// 则返回 false，调用方接着走通用的 upstream_error 分支。透传与转换两条路共用。
//
// 认下的三档一律走 rec.QueueRejected：它顺带把出站端点清回空串（#20）——那一格在
// Do 之前一刻就记上了，而闸在 Do 里面、拨号之前就回绝，这三档一个字节都没到上游，
// 同 401 / 429 / 501 那批。返回 false 那档不走它，那是真打过上游之后的失败
// （拨不通、读超时）。
func (x *Client) writeQueueReject(w http.ResponseWriter, req Request, err error) bool {
	rec, channel := req.Rec, req.Cand.ChannelName
	var word calllog.Outcome
	var msg string
	switch {
	case errors.Is(err, upstream.ErrQueueFull):
		word, msg = calllog.QueueFull, "渠道并发已满，请稍后重试"
	case errors.Is(err, upstream.ErrQueueTimeout):
		word, msg = calllog.QueueTimeout, "渠道并发排队超时，请稍后重试"
	case errors.Is(err, upstream.ErrQueueAbandoned):
		// 客户端在排队途中自己断了：没人在听，不写错误体；状态记 499（nginx 的
		// client closed request 惯例码），流水靠 error=queue_abandoned 归因，
		// 与「打到上游后失败」（upstream_error）分开——这种请求没碰过上游。
		rec.QueueRejected(calllog.QueueAbandoned)
		x.Log.Info("排队途中客户端断开", "channel", channel,
			"queue_wait_ms", rec.QueueWaitMs())
		w.WriteHeader(499)
		return true
	default:
		return false
	}
	rec.QueueRejected(word)
	x.Log.Warn("渠道并发闸拒绝", "channel", channel, "reason", word.String(),
		"queue_wait_ms", rec.QueueWaitMs())
	// Retry-After 要赶在 WriteError 之前设（同 rateLimit）：那里面就 WriteHeader
	// 了，之后再往 Header() 里写什么都不会发出去。回 429 而不是 503：对 Codex 这类
	// harness 429 是「稍后重试」，503 是「换地方」，闸满要的是前者（口径层 v0.50）。
	w.Header().Set("Retry-After", x.QueueRetryAfter)
	req.Inbound.WriteError(w, http.StatusTooManyRequests, msg)
	return true
}
