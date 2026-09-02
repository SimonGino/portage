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

// Result 是一次拿到响应头之后的交换现场：状态行加一个响应观察者。收场统一走
// Close（挂进调用方的 defer），顺序在观察者的构造里定死。
type Result struct {
	Status int
	Header http.Header
	*ResponseObserver
}

// ResponseObserver 包住上游响应体与挂在它上面的观察者（Tap、LogBodies 收集器、
// 错误原文旁路），并把 #8 的收场序做进构造：Close 一律按「先断上游 → 排空解码侧
// 到通道关闭 → 才 Summarize」执行。此前这条序活在 abortDecode 的注释与调用顺序里，
// 靠「写出失败分支记得先调它再 panic」维持；现在收场只有 Close 一个入口，panic
// 展开里的 defer 走的就是同一条序，想绕都绕不开。
type ResponseObserver struct {
	// Body 是给调用方消费的读端：观察者都已经 tee 在上面，拿到的是与转发**同一份**
	// 字节，且都写不坏转发——它们的 Write 恒不报错，io.MultiWriter 因此也不会。
	Body io.Reader

	upstream io.Closer
	rec      *calllog.Recorder
	tap      protocol.Tap
	// events 是转换路径挂上来的解码侧出口（AttachStream）。非空时它是「上游字节
	// 还有另一条线在读」的证据，收场必须先排空它才能 Summarize。
	events <-chan protocol.Event
}

// AttachStream 把解码侧的事件通道挂进收场序（转换流式路径）。解码 goroutine 在
// 另一条线上读 Body，Tap 的写因此也在那条线上——从这一刻起，Close 的排空步骤
// 就是 Summarize 对那些写入的 happens-before 边（#8）。
func (o *ResponseObserver) AttachStream(events <-chan protocol.Event) { o.events = events }

// Close 收场，次序即同步语义（#8）：
//
//  1. 先断上游——解码 goroutine 的下一次 Read 立刻出错收摊，排空因此有界，
//     不会把这次调用挂在一条还在灌数据的连接上；
//  2. 把事件通道排空**到关闭**——通道关闭给出解码侧全部写入对本侧的
//     happens-before 边，Tap 与 LogBodies 收集器两处都零锁；
//  3. 之后 Summarize 才安全，读到的是解码侧写完的最终状态（上游连接断掉前
//     收到的全部字节）。
//
// 没挂解码侧（透传、缓冲、错误早退）时它退化成 Summarize + 断上游：那些路上
// Tap 的写发生在调用方自己的读循环里，走到这里早已停止。正常收完的流走的也是
// 同一条序——通道已关、排空即返回，多关一次上游 body 无害。
func (o *ResponseObserver) Close() {
	o.upstream.Close()
	// nil 通道 range 会永久阻塞，没挂解码侧时必须跳过——不是优化是正确性。
	if o.events != nil {
		for range o.events {
		}
	}
	if o.tap != nil {
		o.rec.Summarized(o.tap.Summary())
	}
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
		status, msg := transportStatus(err, req.Cand.ChannelName)
		x.Log.Error("上游请求失败", "channel", req.Cand.ChannelName, "status", status, "err", upstream.Redact(err))
		req.Inbound.WriteError(w, status, msg)
		return nil, false
	}
	// 在写响应头之前取：之后客户端侧的 Header() 里也有同一个值，但从上游的
	// resp.Header 拿才是「上游报的」，不受本地补头（X-Accel-Buffering）干扰。
	// 只取两档头候选，最终取哪个由流水收尾时定——中间那档在错误体里（v0.74）。
	rec.RequestIDs(upstream.RequestIDs(resp.Header))

	obs := &ResponseObserver{upstream: resp.Body, rec: rec}
	var observers []io.Writer
	if tap := taps.New(req.Cand.Protocol, req.Stream); tap != nil {
		observers = append(observers, tap)
		obs.tap = tap
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
	obs.Body = resp.Body
	if len(observers) > 0 {
		obs.Body = io.TeeReader(resp.Body, io.MultiWriter(observers...))
	}
	return &Result{Status: resp.StatusCode, Header: resp.Header, ResponseObserver: obs}, true
}

// transportStatus 把传输层失败分成两档（口径层 v1.16）：等超时回 504，其余回 502。
//
// 判据只认 net.Error.Timeout()——拨号超时、TLS 握手超时、ResponseHeaderTimeout，
// 加客户端 ctx 的 deadline。连不上、TLS 证书错、EOF 这些是「这条链路坏了」，重打
// 过几次也仍是 502：客户端对这两档的重试策略不一样，混成一个码等于让它猜。
//
// 「重试耗尽」不单独成一档：超时本就从不重试（upstream.retriable），所以耗尽预算的
// 一律是上面那批非超时错误，按链路坏算。文案跟着状态码走——只有码没有话，光看
// 响应体的人还是分不出这两件事。
func transportStatus(err error, channel string) (int, string) {
	if upstream.Timeout(err) {
		return http.StatusGatewayTimeout, "上游渠道 " + channel + " 响应超时"
	}
	return http.StatusBadGateway, "上游渠道 " + channel + " 请求失败"
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
