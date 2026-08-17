// Package server wires the inbound HTTP endpoints to the passthrough relay.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/admin"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/taps"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// writeDeadline 是单次向客户端写出的上限，每写一块推进一次。它约束的是「客户端
	// 收得多慢」，不是「流总共多长」——所以长流不会被它掐断，挂死的慢客户端会。
	writeDeadline = 30 * time.Second

	// copyBufferSize 是透传的读写块大小。按字节块复制、永不按帧切分：透传路径对 SSE
	// 帧边界一无所知，因此并行工具调用那种远超缓冲区的大参数帧也不会被截断。
	copyBufferSize = 32 * 1024
)

// relayBody 把上游响应按字节块复制给客户端，每块 flush 一次。
//
// 不用 io.Copy：它不 flush，SSE 帧会攒在 net/http 的缓冲里，客户端要等攒满或流结束
// 才看得到——正是「逐字输出」失效的成因。也不用 bufio.Scanner 按行读再重组：那会引入
// 换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。
func relayBody(w gin.ResponseWriter, body io.Reader, onFirstByte func()) error {
	rc := http.NewResponseController(w)
	// 先兜住响应头本身与空 body 的情形，之后每写一块再推进一次。
	if err := advanceWriteDeadline(rc); err != nil {
		return err
	}
	buf := make([]byte, copyBufferSize)
	first := true
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if first {
				first = false
				onFirstByte()
			}
			if err := advanceWriteDeadline(rc); err != nil {
				return err
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// setNoBuffering 给 SSE 响应盖上 X-Accel-Buffering: no（口径层 v0.30 裁定）。
//
// nginx 认这个头，见到它就对本次响应关掉 proxy_buffering。加它是因为网关多半跑在
// 一份**不由我们维护**的 nginx 后面：实测（展开层 §11.3）里「关掉缓冲」单独就足以
// 让攒住的 SSE 恢复逐条下发，这个头等于把那一下做进网关自己，不必指望前面那份配置
// 写对了。对不认它的反代与直连客户端是一个无害的多余头。
//
// 它是透传路径上唯一一处「上游没发、我们加上」的响应头——与「透传保真优先」有张力，
// 故走 PO 裁决而非实现侧自决。
func setNoBuffering(h http.Header) {
	h.Set("X-Accel-Buffering", "no")
}

// isEventStream 判 Content-Type 是不是 SSE。用前缀而不是等值：真实上游发的是
// `text/event-stream; charset=utf-8` 这类带参数的形式。
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// advanceWriteDeadline 把「这一次写出」的截止时间往后推。ErrNotSupported 说明底层
// writer 不支持 deadline（本项目的 gin ResponseWriter 支持），不该因此中断透传。
func advanceWriteDeadline(rc *http.ResponseController) error {
	if err := rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

type Server struct {
	cfg config.Config
	db  *sql.DB
	up  *upstream.Client
	log *slog.Logger
	// lim 是全局令牌桶，nil 即不限流（rate_limit_qps 配 0）。
	lim *rate.Limiter
	// queueRetryAfter 是并发闸 429 的 Retry-After 值（整秒字符串），启动时换算一次。
	queueRetryAfter string
}

func New(cfg config.Config, db *sql.DB, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	retry := upstream.RetryPolicy{
		MaxRetries:  cfg.Retry.MaxRetries,
		MaxAttempts: cfg.Retry.MaxAttempts,
		BaseDelay:   cfg.Retry.BaseDelay,
		MaxDelay:    cfg.Retry.MaxDelay,
	}
	// 限流桶在这里造一次、四条转发路由共用。若改成在 rateLimit(ep) 里 new，
	// 每个端点各得一只桶，全局 10 QPS 会变成 40 QPS——而且看不出来。
	s := &Server{
		cfg: cfg, db: db, up: upstream.NewClient(retry), log: log,
		lim: newLimiter(cfg.RateLimitQPS, cfg.RateLimitBurst),
	}
	// key 层内环的 401 摘除挂在这里接到库上（口径层 v0.38）。upstream 不认识
	// *sql.DB，也不该认识——它只知道「这份凭证坏了」，怎么记是 store 的事。
	s.up.Disable = s.disableCredential
	// 渠道并发闸的排队参数（口径层 v0.50）走同一个挂法。Retry-After 在这里就换算
	// 成整秒字符串：它的单位是整秒，不足 1 秒的配置向上顶成 1，回一个 0 等于让
	// 客户端立刻再撞一次闸。
	s.up.Queue = upstream.QueuePolicy{Factor: cfg.Queue.Factor, Wait: cfg.Queue.Wait}
	s.queueRetryAfter = strconv.Itoa(max(1, int(cfg.Queue.RetryAfter/time.Second)))
	return s
}

// writeQueueReject 译写渠道并发闸的三种收场（口径层 v0.50/v0.52）；不是闸的错误
// 则返回 false，调用方接着走通用的 upstream_error 分支。透传与转换两条路共用。
func (s *Server) writeQueueReject(c *gin.Context, rec *callRecord, ep protocol.Endpoint, channel string, err error) bool {
	var word, msg string
	switch {
	case errors.Is(err, upstream.ErrQueueFull):
		word, msg = "queue_full", "渠道并发已满，请稍后重试"
	case errors.Is(err, upstream.ErrQueueTimeout):
		word, msg = "queue_timeout", "渠道并发排队超时，请稍后重试"
	case errors.Is(err, upstream.ErrQueueAbandoned):
		// 客户端在排队途中自己断了：没人在听，不写错误体；状态记 499（nginx 的
		// client closed request 惯例码），流水靠 error=queue_abandoned 归因，
		// 与「打到上游后失败」（upstream_error）分开——这种请求没碰过上游。
		rec.outcome = "queue_abandoned"
		s.log.Info("排队途中客户端断开", "channel", channel,
			"queue_wait_ms", rec.queueWait.Milliseconds())
		c.Writer.WriteHeader(499)
		return true
	default:
		return false
	}
	rec.outcome = word
	s.log.Warn("渠道并发闸拒绝", "channel", channel, "reason", word,
		"queue_wait_ms", rec.queueWait.Milliseconds())
	// Retry-After 要赶在 WriteError 之前设（同 rateLimit）：那里面就 WriteHeader
	// 了，之后再往 Header() 里写什么都不会发出去。回 429 而不是 503：对 Codex 这类
	// harness 429 是「稍后重试」，503 是「换地方」，闸满要的是前者（口径层 v0.50）。
	c.Writer.Header().Set("Retry-After", s.queueRetryAfter)
	ep.Proto.WriteError(c.Writer, http.StatusTooManyRequests, msg)
	return true
}

// disableCredential 摘掉一份 401 的上游凭证。
//
// 只摘 401（口径层 v0.38）：403 在上游还可能是「这把凭证没开通这个模型」，摘掉的
// 却是渠道级资源，误伤代价不对称。摘除只人工恢复，所以这里不留任何自动恢复的钩子。
//
// 不用请求 ctx：它可能因为客户端断开而已经取消，而那时摘除更该落下——一把确定性
// 失效的凭证留在池子里，下一个请求还会再吃一次 401。落库失败只记日志：这一步失败
// 不该把一次**已经换到好凭证并成功**的转发变成客户端眼里的错误。
func (s *Server) disableCredential(cred store.Credential, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.DisableCredential(ctx, s.db, cred.ID, reason); err != nil {
		s.log.Error("摘除上游凭证失败", "credential", cred.Name, "err", err)
		return
	}
	// 只报名字，不报凭证值：日志会进文件、进采集、被贴进 issue，跟管理端里那个
	// 要登录才看得到的回读（v0.47）不是一回事。
	s.log.Warn("上游凭证已停用，需人工恢复", "credential", cred.Name, "reason", reason)
}

func (s *Server) Engine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(s.recovery())
	// /healthz 不鉴权：它是给反代与容器编排探活用的，那些探针没地方放 key，
	// 而它只回一个「库还连得上吗」，不泄露任何配置。
	r.GET("/healthz", s.healthz)
	r.GET("/v1/models", s.authModels(), s.models)
	for _, ep := range []protocol.Endpoint{
		protocol.EndpointMessages,
		protocol.EndpointCountTokens,
		protocol.EndpointChatCompletions,
		protocol.EndpointResponses,
	} {
		// 顺序即语义：日志层最外，鉴权失败也落得下那一行；限流在鉴权之后，
		// 理由见 rateLimit 的注释。
		r.POST(ep.Path, s.callLog(ep), s.authRelay(ep), s.rateLimit(ep), s.relay(ep))
	}
	// legacy 的 v1 compact 明确回 501（口径层 v0.54），不落到 SPA 的 NoRoute 上去。
	r.POST("/v1/responses/compact", compactUnsupported)
	// 管理面自己挂自己的路由与鉴权（cookie 会话），与上面这套 key 鉴权互不相干。
	// 它同时接管 NoRoute 来发 SPA，所以必须在全部业务路由注册完之后调。
	admin.New(s.db, s.log).Mount(r)
	return r
}

// recovery 与 gin.Recovery 只差一处：http.ErrAbortHandler 原样再抛给 net/http。
//
// gin 把它归进 broken pipe 分支 recover 掉，于是响应被正常收尾——chunked 的终止块
// 照发，客户端看到的是一个「干净结束」的流。而透传中途失败时我们要的恰恰相反：
// 连接必须异常终止，客户端才能区分「上游说完了」和「上游死了」。net/http 自己的
// recover 认得 ErrAbortHandler，会静默断连，正是这个语义。
func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			s.log.Error("handler panic", "path", c.Request.URL.Path, "panic", rec)
			if !c.Writer.Written() {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			c.Abort()
		}()
		c.Next()
	}
}

func (s *Server) healthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// models 按 OpenAI models 列表格式列出**全部可路由的模型名**——harness 启动时会拉它。
//
// 两半：未停用的接入点名，加上可用纳管模型的限定名 `渠道名/纳管模型名`（口径层
// v0.32）。这条列表的唯一契约是「列出来的都调得通」，所以它必须和 store.Resolve
// 认的名字集合逐字一致，改一边就得改另一边。
//
// 不按 key 的 allowed_models 过滤：白名单校验只在转发端（口径层 v0.28），因此一把
// 受限 key 能在这里看到它调不了的名字。
func (s *Server) models(c *gin.Context) {
	models, err := store.ListExposedModels(c.Request.Context(), s.db)
	if err != nil {
		s.log.Error("列可路由模型失败", "err", err)
		protocol.OpenAI.WriteError(c.Writer, http.StatusInternalServerError, "模型列表读取失败")
		return
	}

	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, gin.H{
			"id":       m.ID,
			"object":   "model",
			"created":  m.CreatedAt,
			"owned_by": "portage",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// requestHead is the only part of the client body the gateway parses. Everything
// forwarded upstream is still the original bytes — no re-marshal.
type requestHead struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func (s *Server) relay(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录由 callLog 中间件建、也由它落——鉴权失败时 relay 压根不执行，
		// 日志逻辑留在这里就等于 401 不落库（#22）。
		rec := callRecordFrom(c)

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "读取请求体失败")
			return
		}
		if s.cfg.LogBodies {
			rec.requestBody = newCapture(bodyCaptureLimit)
			_, _ = rec.requestBody.Write(body)
		}

		var head requestHead
		if err := json.Unmarshal(body, &head); err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		if head.Model == "" {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体缺少 model 字段")
			return
		}
		rec.requestedModel, rec.stream = head.Model, head.Stream

		// 白名单校验放在这儿而不是鉴权中间件：那一层跑的时候请求体还没读，
		// 不知道要判哪个模型。403 而不是 404——这把 key 不能用它，不是它不存在，
		// 说成 404 会把人引去查配置。
		//
		// 逐项精确匹配，接入点名与纳管模型限定名都可以写在白名单里（口径层 v0.32）：
		// 两种名都能路由，白名单只管一种就等于留了条绕过去的路。Allows 本身不用改，
		// 它比的就是这个字符串。
		if key := apiKeyFrom(c); !key.Allows(head.Model) {
			rec.outcome = "model_not_allowed"
			ep.Proto.WriteError(c.Writer, http.StatusForbidden,
				"当前 key 不允许访问模型 "+head.Model)
			return
		}

		// 入站协议参与解析：渠道声明的是一个支持协议集，选哪个由「能透传就透传」
		// 决定（口径层 v0.33）。同一个渠道、同一个模型，`/v1/responses` 进来走上游
		// Responses，`/v1/chat/completions` 进来走上游 CC，客户端不用在模型名里标。
		cand, err := store.Resolve(c.Request.Context(), s.db, head.Model, ep.Proto)
		switch {
		case errors.Is(err, store.ErrAccessPointNotFound):
			// 这里说「模型」而不是「接入点」：客户端填的可能是限定名，报成
			// 「接入点不存在」会把人引去接入点页面找一个本来就不该存在的条目。
			ep.Proto.WriteError(c.Writer, http.StatusNotFound, "模型 "+head.Model+" 不存在或已停用")
			return
		case errors.Is(err, store.ErrNoUsableCandidate):
			ep.Proto.WriteError(c.Writer, http.StatusServiceUnavailable, "模型 "+head.Model+" 当前没有可用的上游")
			return
		case err != nil:
			s.log.Error("模型解析失败", "model", head.Model, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "模型解析失败")
			return
		}

		rec.channel, rec.channelProto, rec.upstreamModel = cand.ChannelName, cand.Protocol, cand.UpstreamModel

		// Codex 压缩闸（口径层 v0.54，#71）：拦在选完渠道之后、分岔之前——判据要同时
		// 用到「渠道说哪个协议」与「它认不认 compaction_trigger」，而两条路的收场是
		// 同一句拒绝，没有理由在分岔两侧各写一遍。见 compaction.go。
		if s.rejectCompaction(c, rec, ep, cand, body) {
			return
		}

		// 转换闸。#80 九宫格全开后，这里能拦下的只剩「没有上游对应端点」的入口端点
		// （count_tokens），501 是**没得做**而不是「还没做」——见 conversionOpen 的注释。
		if cand.Protocol != ep.Proto {
			if !conversionOpen(ep, cand.Protocol) {
				ep.Proto.WriteError(c.Writer, http.StatusNotImplemented,
					"该端点没有对应的转换路径："+ep.Path+" → "+string(cand.Protocol))
				return
			}
			s.relayConverted(c, rec, ep, cand, body, head.Stream)
			return
		}

		// 接入点对外模型名 → 纳管模型名（口径层 §2.3）。字节级 splice，不整体重编码。
		forward, err := protocol.RewriteModel(body, cand.UpstreamModel)
		if err != nil {
			s.log.Error("改写 model 字段失败", "channel", cand.ChannelName, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体 model 字段无法改写")
			return
		}

		resp, at, err := s.up.Do(c.Request.Context(), cand, ep, c.Request.URL.RawQuery, forward, c.Request.Header, head.Stream)
		rec.retries, rec.channelKey, rec.queueWait = at.Retries(), at.Credential, at.QueueWait
		if err != nil {
			if s.writeQueueReject(c, rec, ep, cand.ChannelName, err) {
				return
			}
			// 只报渠道名；Redact 摘掉传输错误里内嵌的 base_url。
			rec.outcome = "upstream_error"
			// 这一支没有响应体可截，落库的原文就是这条传输错误本身（口径层 v0.53）。
			// 不落的话，最想看细节的那半边——连不上、握手失败、读超时——恰好永远是空。
			rec.setErrorDetail(upstream.Redact(err).Error())
			s.log.Error("上游请求失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
			ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+cand.ChannelName+" 请求失败")
			return
		}
		defer resp.Body.Close()
		// 在写响应头之前取：之后 c.Writer.Header() 里也有同一个值，但从上游的
		// resp.Header 拿才是「上游报的」，不受本地补头（X-Accel-Buffering）干扰。
		// 只取两档头候选，最终取哪个由 logCall 收尾时定——中间那档在错误体里（v0.74）。
		rec.officialRequestID, rec.proxyRequestID = upstream.RequestIDs(resp.Header)

		// Tap 与 body 记录都挂旁路：拿到的是与转发**同一份**字节，且都写不坏
		// 转发——它们的 Write 恒不报错，io.MultiWriter 因此也不会。
		var observers []io.Writer
		if tap := taps.New(cand.Protocol, head.Stream); tap != nil {
			observers = append(observers, tap)
			defer func() {
				rec.summary, rec.haveSummary = tap.Summary(), true
			}()
		}
		if s.cfg.LogBodies {
			rec.responseBody = newCapture(bodyCaptureLimit)
			observers = append(observers, rec.responseBody)
		}
		// 上游说不行时，把它说的话截一段落库（口径层 v0.53）。挂旁路而不是先读后转：
		// 透传路径上响应字节属于客户端，不能为了记一份错误体把它先攒进内存。
		// 判据是状态码而非 error 列——透传 4xx 的 error 列是空的（v0.28 纪律）。
		if resp.StatusCode >= 400 {
			rec.errorDetail = newCapture(upstreamErrorLimit)
			observers = append(observers, rec.errorDetail)
		}
		src := io.Reader(resp.Body)
		if len(observers) > 0 {
			src = io.TeeReader(resp.Body, io.MultiWriter(observers...))
		}

		upstream.CopyResponseHeaders(c.Writer.Header(), resp.Header)
		// 只在这一处偏离「原样透传上游响应头」：SSE 时补 X-Accel-Buffering: no。
		// 补在 CopyResponseHeaders 之后是有意的——上游若自己发了这个头，以我们的为准。
		if isEventStream(c.Writer.Header().Get("Content-Type")) {
			setNoBuffering(c.Writer.Header())
		}
		c.Writer.WriteHeader(resp.StatusCode)
		rec.outcome = "ok"
		if err := relayBody(c.Writer, src, func() { rec.firstByte = time.Now() }); err != nil {
			// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
			rec.outcome = "stream_aborted"
			s.log.Warn("首字节写出后透传中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
			panic(http.ErrAbortHandler)
		}
	}
}
