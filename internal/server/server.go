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
	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/exchange"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

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

type Server struct {
	cfg config.Config
	db  *sql.DB
	// ex 是「一次上游交换」的编排方（#9）：dial→attempt→错误收场→观察者装配都在
	// 它里面，透传与转换两条 relay 只当 adapter。
	ex  *exchange.Client
	log *slog.Logger
	// genLim 是生成面那三个端点共用的全局令牌桶，nil 即不限流（rate_limit_qps 配 0）。
	genLim *rate.Limiter
	// countTokensLim 是 count_tokens 独占的那只（#16），配置同 genLim。选桶见 pickLimiter。
	countTokensLim *rate.Limiter
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
	// 限流桶在这里造，**两只**：生成面那三条转发路由共用 genLim，count_tokens 独占
	// countTokensLim（#16，理由见 pickLimiter）。桶数是刻意停在 2 的——若改成在
	// rateLimit(ep) 里逐端点 new，四个端点各得一只，配 10 QPS 实收 40 而且看不出来。
	s := &Server{
		cfg: cfg, db: db, log: log,
		genLim:         newLimiter(cfg.RateLimitQPS, cfg.RateLimitBurst),
		countTokensLim: newLimiter(cfg.RateLimitQPS, cfg.RateLimitBurst),
	}
	up := upstream.NewClient(retry)
	// 渠道并发闸的排队参数（口径层 v0.50）在这里从配置接上。Retry-After 在这里就换算
	// 成整秒字符串：它的单位是整秒，不足 1 秒的配置向上顶成 1，回一个 0 等于让
	// 客户端立刻再撞一次闸。
	up.Queue = upstream.QueuePolicy{Factor: cfg.Queue.Factor, Wait: cfg.Queue.Wait}
	s.ex = &exchange.Client{
		Up: up, Log: log, LogBodies: cfg.LogBodies,
		QueueRetryAfter: strconv.Itoa(max(1, int(cfg.Queue.RetryAfter/time.Second))),
	}
	return s
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
	//
	// **形态闸（口径层 §2.9 #27）：没有管理密码就没有管理面。** 整个 Mount 不调，于是
	// /admin 页面、/admin/api/* 与登录会话全部**不注册**——404 是路由级的，不是鉴权级。
	//
	// 口径一句话：**值只用于初始化，存在性用于形态**。这是给 v0.28「密码只用来初始化」
	// 加一半，不推翻它。
	//
	// 闸只看 cfg（配置文件或 PORTAGE_ADMIN_PASSWORD），**不看库**：形态是进程属性，
	// 而库是共享资源——看库的话，一台带 UI 的观测实例往 settings 里写的密码，会把生产
	// 那台纯转发机的管理面一起打开。
	//
	// 顺带一提 webui 那个 build tag 管的是**另一件事**：它只决定前端资产 embed 不 embed，
	// 路由本来是无条件挂的，凭证回读接口照常活着。「编译期去掉 UI」从来不等于「有了
	// 无 UI 的部署形态」，这道运行期的闸才是。
	if strings.TrimSpace(s.cfg.AdminPassword) != "" {
		admin.New(s.db, s.log, s.cfg.Declarative).Mount(r)
	}
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
		// 日志逻辑留在这里就等于 401 不落库（portage-legacy#22）。
		rec := recorderFrom(c)

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "读取请求体失败")
			return
		}
		if s.cfg.LogBodies {
			rec.RecordRequestBody(body)
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
		rec.RequestParsed(head.Model, head.Stream)

		// 白名单校验放在这儿而不是鉴权中间件：那一层跑的时候请求体还没读，
		// 不知道要判哪个模型。403 而不是 404——这把 key 不能用它，不是它不存在，
		// 说成 404 会把人引去查配置。
		//
		// 逐项精确匹配，接入点名与纳管模型限定名都可以写在白名单里（口径层 v0.32）：
		// 两种名都能路由，白名单只管一种就等于留了条绕过去的路。Allows 本身不用改，
		// 它比的就是这个字符串。
		if key := apiKeyFrom(c); !key.Allows(head.Model) {
			rec.Refused(calllog.ModelNotAllowed)
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

		rec.Routed(cand.ChannelName, cand.Protocol, cand.UpstreamModel)

		// 输入上限闸（口径层 v0.99）：判**选中候选**的限，不筛候选、不触发转移。
		// 判据是入站原始 body 字节数 ÷ 4 的估算——透传路径不解析 body 是硬约束，
		// 字节估算是唯一让透传与转换同一把尺的算法（RewriteModel 的 splice 与转换
		// 的 decode 都在闸后，估的都是这份入站字节）。count_tokens 豁免：判端点不判
		// 协议（pickLimiter/conversionOpen 踩过的同一个坑），那条路不打上游生成侧，
		// 且它正是客户端用来自行判断「要不要压缩」的工具。
		if est := len(body) / 4; cand.MaxInputTokens > 0 && est > cand.MaxInputTokens &&
			ep != protocol.EndpointCountTokens {
			rec.Refused(calllog.RequestTooLarge)
			ep.Proto.WriteError(c.Writer, http.StatusRequestEntityTooLarge,
				"请求过大：估算输入 "+strconv.Itoa(est)+" token，超出模型 "+head.Model+
					" 的输入上限 "+strconv.Itoa(cand.MaxInputTokens)+"（按请求体字节数估算，不精确）")
			return
		}

		// Codex 压缩闸（口径层 v0.54）：拦在选完渠道之后、分岔之前——判据要同时
		// 用到「渠道说哪个协议」与「它认不认 compaction_trigger」，而两条路的收场是
		// 同一句拒绝，没有理由在分岔两侧各写一遍。见 compaction.go。
		if s.rejectCompaction(c, rec, ep, cand, body) {
			return
		}

		// Responses 有状态续链闸（口径层 v0.88），位置同上：它只管**透传**那半边，
		// 转换那半边由 codec 在 DecodeRequest 里无条件拒。这两条闸与下面的 RewriteModel
		// 400 一样都在 rec.Dialing 之前——它们一个字节都没打上游。见 stateful.go。
		if s.rejectStatefulResponses(c, rec, ep, cand, body) {
			return
		}

		// count_tokens 撞非 anthropic 渠道走本地估算（#18，口径层 v0.80）：CC /
		// Responses 没有原生端点可转发，此前这一格回 501。判据是**端点**不是入口
		// 协议——它与 /v1/messages 的 ep.Proto 同为 anthropic（conversionOpen 与
		// pickLimiter 都踩过同一个坑）。拦在转换闸之前：它不是一条转换路径，是一条
		// 不打上游的本地路。
		if cand.Protocol != ep.Proto && ep == protocol.EndpointCountTokens {
			s.countTokensLocal(c, rec, ep, cand, body)
			return
		}

		// 转换闸。portage-legacy#80 九宫格全开、count_tokens 又在上面拆去本地路之后，
		// 今天没有能走到这条 501 的组合——留着它是给将来新端点兜底：既没有上游对应
		// 端点、也没有本地路的入口，落进来该被明确拒掉而不是乱转。
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

		// 同协议透传的出站端点就是入站那条（#20）：这条路上入口即出口，count_tokens
		// 透传到 anthropic 渠道打的也还是 /v1/messages/count_tokens。RawQuery 整串
		// 照抄（portage-legacy#20）；错误原文挂旁路占坑（TapErrorBody）——透传路径上
		// 响应字节属于客户端，不能为了记一份错误体把它先攒进内存。
		res, ok := s.ex.Do(c.Request.Context(), c.Writer, exchange.Request{
			Rec: rec, Inbound: ep.Proto, Cand: cand, Endpoint: ep,
			RawQuery: c.Request.URL.RawQuery, Body: forward, Header: c.Request.Header,
			Stream: head.Stream, TapErrorBody: true,
		})
		if !ok {
			return
		}
		defer res.Close()

		upstream.CopyResponseHeaders(c.Writer.Header(), res.Header)
		// 只在这一处偏离「原样透传上游响应头」：SSE 时补 X-Accel-Buffering: no。
		// 补在 CopyResponseHeaders 之后是有意的——上游若自己发了这个头，以我们的为准。
		if isEventStream(c.Writer.Header().Get("Content-Type")) {
			setNoBuffering(c.Writer.Header())
		}
		c.Writer.WriteHeader(res.Status)
		rec.Succeeded()
		if err := exchange.NewWriter(c.Writer, rec.FirstByte).Copy(res.Body); err != nil {
			// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
			rec.Failed(calllog.StreamAborted, "")
			s.log.Warn("首字节写出后透传中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
			panic(http.ErrAbortHandler)
		}
	}
}

// insertCallLog 是 Server 接给流水的落库出口（calllog.Sink）。
//
// 一层薄适配：本包知道 *sql.DB 在哪，流水那一侧只知道「有一行要落」。
func (s *Server) insertCallLog(ctx context.Context, row calllog.Row) error {
	return store.InsertCallLog(ctx, s.db, row)
}
