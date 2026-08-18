package calllog

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// Recorder 是一次调用的流水记账本：沿途按事件收，收尾时落一行 slog 加一行
// call_logs。由最外层的 callLog 中间件建、也由它的 defer 收尾——放在中间件而不是
// relay 里，是因为鉴权失败的请求也要留痕。
//
// 里面**没有**渠道 base_url 与凭证，也不该被加进来：日志是最容易被复制粘贴出去的
// 东西，上游密钥不进日志是硬约束。渠道用 name 指代已经够定位。
type Recorder struct {
	// log 与 sink 是收尾时的两个出口。Detached 的那条两者皆 nil，写进去的东西
	// 哪儿也不去——那正是「取不到记录」该有的样子。
	log  *slog.Logger
	sink Sink
	// finished 让收尾幂等：重复落库的表现是账翻倍而所有断言照常绿。
	finished bool
	// misuse 攒下沿途用错半区的收场词（见 complain）。正常路径恒空。
	misuse []string

	start time.Time

	// —— 三段可空映射的判据 ——
	//
	// 这三格回答的是同一类问题：「这件事到底发生过没有」。它们必须是**显式**的
	// 标志位，不能借别的字段的影子去推——借来的判据换个实现就对不上，而且改错了
	// 一条测试都不会红（那正是收编前的原样）。
	//
	// haveRequest：解析到请求头部没有。它决定 is_stream 落值还是落 NULL。
	// haveFirstByte：首字节来过没有。它决定 ttfb_ms 打不打、ttft_ms 落不落。
	//   不看 firstByte.IsZero()——那是 time.Time 的性质不是本模块的语义，何况同一个
	//   表达式在两处的含义还不同（slog 侧「来过就打」，落库侧「流式且来过才落」）。
	// haveSummary：Tap 交过 usage 没有。它决定 token 五列落值还是落 NULL。
	haveRequest   bool
	haveFirstByte bool
	haveSummary   bool

	firstByte time.Time

	endpoint string
	// upstreamEndpoint 是这次**打到上游**的那条路径（#20），由 Dialing 在发请求
	// 之前的那一刻记上——不是在算出出口端点的地方：解不动请求体、编不出出口请求、
	// 压缩闸拒绝那些行都停在发请求之前，它们同样没打上游，这一格该空着。
	//
	// 不变量：**非空 ⟺ 真的向上游发起过**（含拨不通、读超时那些——那些行有
	// error_detail，恰恰要知道打的哪条路径）。反过来，401 / 429 / 501 / 并发闸队满
	// 一律空串（队满那档由 QueueRejected 清回去，见那里）。
	upstreamEndpoint string
	// apiKeyName 是网关 key 的**名字**，不是 key 本身，也不是它的 hash：
	// 日志是最容易被复制粘贴出去的东西，凭证材料一概不进。
	apiKeyName     string
	inboundProto   protocol.Protocol
	requestedModel string
	channel        string
	// channelKey 是本次**真正发出请求**的那份上游凭证的名字（换过凭证时是最后一
	// 份，失败亦然）。名字不是凭证值——网关不从凭证值派生任何显示字符（口径层
	// v0.38），日志里出现的永远只是人自己写的那个名字。
	channelKey    string
	channelProto  protocol.Protocol
	upstreamModel string
	stream        bool
	status        int
	// outcome 区分「同样是 200」的几种收场，尤其是首字节之后断流那种——
	// 状态码已经发出去了，只有这里能看出它其实没说完。
	//
	// 词表与「哪个词落库、哪个词留 NULL」都关在 Outcome 里（口径层 v0.70）。
	outcome Outcome
	// retries 是这次调用向上游重打了几次（含换凭证之后的那些，口径层 v0.38）。
	// 不含首次尝试，0 是常态所以不打进日志——否则每一行都要背一个恒为 0 的字段。
	// 非 0 才说明发生过退避重试或换过凭证，「这次怎么慢了三秒」有据可查。
	retries int
	// queueWait 是在渠道并发闸上排队的耗时（口径层 v0.52），没排队为 0。
	// 与 retries 同理，非 0 才进 slog；落库则恒落（列不可空，0 就是 0）。
	queueWait time.Duration

	// upstreamRequestID 是最终落库与进 slog 的那个上游请求 id（口径层 v0.56，#2）。
	// 它同时原样回传给了客户端（透传路径）；留一份在流水里，是为了事后不用翻客户端
	// 日志就能对账。
	//
	// 不是凭证材料，进日志无碍：这是上游自己给这次调用编的号，报障时官方文档就要它。
	//
	// 值由 resolveUpstreamRequestID 在 Finish 时从下面两档候选加错误体里那一档定
	// 下来（口径层 v0.74），别在别处直接赋值。
	upstreamRequestID string
	// officialRequestID 是第一档：响应头里官方拼写的 `request-id`，Anthropic 自己写的。
	officialRequestID string
	// proxyRequestID 是第三档：响应头里的 `x-request-id`。排最后是因为它多半是中间层
	// 给自己编的号——2026-08-15 实测那条链路上它**总是**有值，插在前面就等于永远只
	// 记中转的流水号，拿去问官方什么都查不到，比空着更误导。
	proxyRequestID string

	summary protocol.Summary

	requestBody  *captureWriter
	responseBody *captureWriter

	// errorDetail 是上游错误原文（口径层 v0.53），只在失败时有值。收到
	// upstreamErrorLimit，落库那一刻截到 errorDetailLimit。
	//
	// 是 *captureWriter 而不是 string，因为透传路径上它得挂在旁路上边转发边收——
	// 那条链路不允许为了记一份错误体把响应先读进内存再转出去。另外两个来源（转换
	// 路径的 UpstreamRejected、传输错误的 Failed）都走 setErrorDetail。
	//
	// 它同时是 request_id 第二档的字节来源（口径层 v0.74）：那一档「复用 error_detail
	// 已经读进来的字节」，不新开一次 body 读，因此不违反透传路径不 decode→encode。
	errorDetail *captureWriter
}

// —— 生命周期事件 ————————————————————————————————————————————————
//
// 下面这一组方法是这条流水对外的**全部**写入口。它们不是字段的 setter 包装：
// 一次调用沿途会发生的事就这么些，每件事一个有名字的动词，参数即那件事**当时**
// 才知道的东西。沿途的调用方因此不必知道这些事分别落在哪几个字段上，也就不可能
// 只写一半（漏掉 haveSummary、忘了把 upstreamEndpoint 清回去）。
//
// 顺序不变量在这里也从口口相传变成写下来的：谁先到先算数、谁后写覆盖先写，
// 逐个动词的 doc 里说明。

// New 开一条流水。端点与入站协议在这一刻就写死——鉴权失败、限流那些行
// 别的什么都没有，只有这两格分得开它们。
//
// log 落 slog，sink 落库。两者都可以是 nil（见 Detached），收尾时什么都不做。
//
// outcome 的初值是 Rejected 而不是 OK：走到收尾还没被任何分支覆盖，说明这次调用
// 停在某个早退分支上了，那确实是一次拒绝。反过来设成 OK 的话，任何漏写 outcome
// 的新分支都会静默记成一次干净的成功。
func New(ep protocol.Endpoint, log *slog.Logger, sink Sink) *Recorder {
	return &Recorder{
		start:        time.Now(),
		endpoint:     ep.Path,
		inboundProto: ep.Proto,
		outcome:      Rejected,
		log:          log,
		sink:         sink,
	}
}

// Detached 是「上下文里取不到记录」时的兜底，一个黑洞：动词照常收，
// 收尾什么都不做（没有 logger 也没有 sink）。
//
// 回它而不是 panic：三层中间件是在 Engine() 里一起注册的，取不到只可能是路由被
// 改坏了，那不该顺带把请求也打成 500。日志少一行由 TestEveryRelayEndpointLogsOnce
// 那类用例去逮。
func Detached() *Recorder {
	return &Recorder{start: time.Now(), outcome: Rejected}
}

// Authenticated 记下认出来的网关 key 名。名字不是 key 本身也不是它的 hash——
// 上下文里的东西迟早会被顺手打进日志。
func (r *Recorder) Authenticated(keyName string) { r.apiKeyName = keyName }

// RequestParsed 记下从请求体头部解出来的两格。这两格是同源的：走到这里就说明
// 请求体读得动、是合法 JSON、且带了 model，三者缺一都在这之前返回了。
//
// 「有没有解析到请求」这条判据因此就是「这个动词有没有被调过」（haveRequest），
// 不借 requestedModel 非空去推——那是同一件事的影子，换个实现就对不上了。
//
// 两者今天逐字等价：这个动词挂在「model 为空就 400 返回」那一行的后面，所以
// requestedModel 非空 ⟺ 走到过这里。等价不等于同一件事，判据要说自己的话。
func (r *Recorder) RequestParsed(model string, stream bool) {
	r.requestedModel, r.stream, r.haveRequest = model, stream, true
}

// RecordRequestBody 记一份请求体（log_bodies 打开时）。字节已经拿在手里，
// 不像响应侧那样要挂旁路。
func (r *Recorder) RecordRequestBody(body []byte) {
	r.requestBody = newCapture(bodyCaptureLimit)
	_, _ = r.requestBody.Write(body)
}

// Routed 记下这次落到了哪条渠道、走哪个上游协议、翻成了哪个纳管模型名。
// 只记渠道**名**：base_url 与凭证一概不进这条流水。
func (r *Recorder) Routed(channel string, proto protocol.Protocol, upstreamModel string) {
	r.channel, r.channelProto, r.upstreamModel = channel, proto, upstreamModel
}

// Dialing 记下这次**打到上游**的那条路径，在发请求之前的那一刻调（#20）。
//
// 不变量：这一格非空 ⟺ 真的向上游发起过（含拨不通、读超时那些）。所以它必须
// 挨着发请求那一行调，不能在算出出口端点的地方就调——中间还夹着解不动请求体、
// 编不出出口请求、压缩闸拒绝几条早退，那些行同样没打上游。
func (r *Recorder) Dialing(upstreamEndpoint string) { r.upstreamEndpoint = upstreamEndpoint }

// Attempted 记下一次上游尝试收场后的三个数：重打了几次、最后用的哪份凭证、
// 在并发闸上排了多久。成功失败都要调——失败那些行恰恰最需要知道重打过几次。
//
// 收三个裸参数而不是 upstream.Attempt：本包绝不能 import internal/upstream
// （upstream 已经 import 了 store，而 store 又 import 本包，收进来就成环）。
func (r *Recorder) Attempted(retries int, credential string, queueWait time.Duration) {
	r.retries, r.channelKey, r.queueWait = retries, credential, queueWait
}

// RequestIDs 记下响应头里的两档 request id 候选（口径层 v0.74）。最终取哪一档
// 由收尾时的 resolveUpstreamRequestID 定——中间那档在错误体里，那会儿还没收完。
func (r *Recorder) RequestIDs(official, proxy string) {
	r.officialRequestID, r.proxyRequestID = official, proxy
}

// Summarized 收下 Tap 从上游原始字节里旁路解出来的 usage。
//
// 「有没有 summary」是 token 五列落值还是落 NULL 的判据，所以调没调过这个动词
// 本身就是信息——上游回了一份全零的 usage 与压根没走到上游，不是一回事。
func (r *Recorder) Summarized(sum protocol.Summary) {
	r.summary, r.haveSummary = sum, true
}

// TapResponseBody 开一路响应体旁路（log_bodies 打开时），返回挂进 io.MultiWriter
// 的那个 writer。它永不报错——报错会被 MultiWriter 变成读错误、打断转发。
func (r *Recorder) TapResponseBody() io.Writer {
	r.responseBody = newCapture(bodyCaptureLimit)
	return r.responseBody
}

// TapUpstreamErrorBody 把错误原文的接管权**先占下来**，返回边转发边收的那个 writer。
//
// 与 setErrorDetail 语义不同，这正是它不得不绕过后者的原因：setErrorDetail 是
// 「原文已经拿在手里」，这个是「原文还没产生，先占坑」。透传路径上响应字节属于
// 客户端，不能为了记一份错误体把它先攒进内存，所以只能挂旁路。
//
// **副作用要知道**：占坑之后错误原文就算「已有」，此后 Failed 带来的 detail 不再
// 覆盖它（先到先得）。那是想要的——旁路收的是上游原话，收尾分支给的是一句概括。
func (r *Recorder) TapUpstreamErrorBody() io.Writer {
	r.errorDetail = newCapture(upstreamErrorLimit)
	return r.errorDetail
}

// FirstByte 记下首字节到达的时刻，只认第一次。它是 ttft 的唯一来源——别再拿
// 「这个时刻是不是零值」当业务判据，那是 time.Time 的性质不是这条流水的语义。
func (r *Recorder) FirstByte() {
	if r.haveFirstByte {
		return
	}
	r.haveFirstByte = true
	r.firstByte = time.Now()
}

// Succeeded 把收场记成一次干净的成功。在写出响应头之后调：那一刻格式承诺已经
// 生效，之后再断只能记 stream_aborted，不能退回别的词。
func (r *Recorder) Succeeded() { r.outcome = OK }

// Refused 记一次**网关自己回绝**的收场：鉴权不过、白名单挡下、限流、转换闸、
// 压缩闸。这一半的词见 Outcome.Refusal。
func (r *Recorder) Refused(o Outcome) {
	if !o.Refusal() {
		r.complain("Refused", o)
	}
	r.outcome = o
}

// QueueRejected 记一次渠道并发闸的回绝，并把出站端点**清回空串**。
//
// 清回去是必须的：出站端点在发请求之前那一刻就记上了，而闸在拨号之前就回绝——
// 这三档一个字节都没到上游，同 401 / 429 / 501 那批，空就是事实。清在这一个动词
// 里而不是两个调用点各写一遍，是因为这本来就是那两条路共用的同一件事。
func (r *Recorder) QueueRejected(o Outcome) {
	if !o.Refusal() {
		r.complain("QueueRejected", o)
	}
	r.outcome = o
	r.upstreamEndpoint = ""
}

// Failed 记一次**打过上游之后**的失败，顺带收下一段错误原文（口径层 v0.53）。
// 这一半的词见 Outcome.Failure。
//
// detail 传空串即「这一支没有原文可记」。非空时走先到先得：透传路径的旁路收集器
// 若已经占了坑，这里给的一句概括不覆盖它。
func (r *Recorder) Failed(o Outcome, detail string) {
	if !o.Failure() {
		r.complain("Failed", o)
	}
	r.outcome = o
	r.setErrorDetail(detail)
}

// UpstreamRejected 收下上游的错误体（转换路径：上游回了非 2xx，字节还在流里），
// 记下收场与原文，并把读到的那段字节还给调用方——它还要从里面取一句
// error.message 回给客户端。
//
// 读多少由本包定（upstreamErrorLimit），不让调用方传上限：那个数是「一个巨大的
// HTML 错误页别把内存吃满」与「request_id 在信封末尾、取键要在截断前的字节上做」
// 两条约束的交点，散出去就会有人按落库那个 2KB 去读。
//
// 与 TapUpstreamErrorBody 是同一件事在两条链路上的两个名字，区别只是谁先拿到
// 字节：透传那边要占坑边收边转，这边可以一口气读完。合并它们要求两条链路的
// 错误处理先长得一样，那是 #9（attempt module）的活，不在这一层做。
func (r *Recorder) UpstreamRejected(body io.Reader) []byte {
	// 读错误不额外处理：读到多少算多少，那段字节本来就只是给人看的排障材料。
	raw, _ := io.ReadAll(io.LimitReader(body, upstreamErrorLimit))
	r.outcome = UpstreamError
	r.setErrorDetail(string(raw))
	return raw
}

// QueueWaitMs 是排队耗时的毫秒数，给闸拒那两条 slog 用。
//
// 这是这条流水唯一露出去的**读**口子：那两条日志要报「排了多久才被拒」，而这个数
// 只有这里有。其余字段一律不开读口——流水的读者是库里那一行与收尾那一行 slog，
// 沿途谁都不该回头查自己刚记了什么。
func (r *Recorder) QueueWaitMs() int64 { return r.queueWait.Milliseconds() }

// resolveUpstreamRequestID 按三档定下这次调用的上游请求 id（口径层 v0.74）：
// `request-id`（头）→ 错误响应体里的 `request_id` → `x-request-id`（头）。
//
// 前两档都是 Anthropic 自己写的，第三档是中间层给自己编的号，所以新档插在中间。
//
// 在收尾时才做而不是拿到响应头就做，是因为第二档要等错误体收完——透传路径上那份字节
// 是边转发边攒的，最后一个字节到这一刻才齐。
//
// 第二档只有失败行有货：errorDetail 只在失败时才被挂上（透传路径按 status >= 400
// 走 TapUpstreamErrorBody，转换路径走 UpstreamRejected），成功行走不到这里的 json 解析。
func (r *Recorder) resolveUpstreamRequestID() string {
	if r.officialRequestID != "" {
		return r.officialRequestID
	}
	if r.errorDetail != nil {
		if id := ErrorBodyRequestID(r.errorDetail.collected()); id != "" {
			return id
		}
	}
	return r.proxyRequestID
}

// setErrorDetail 记下一段已经拿在手里的错误原文。**不覆盖已有的**：透传路径的旁路
// 收集器先挂上，之后的收尾分支不该把它抹成一句概括。
func (r *Recorder) setErrorDetail(s string) {
	if r.errorDetail != nil || s == "" {
		return
	}
	r.errorDetail = newCapture(upstreamErrorLimit)
	_, _ = r.errorDetail.Write([]byte(s))
}

// Sink 是这条流水的落库出口。
//
// 是个函数类型而不是直接调 store：本包被 store import（写侧行类型住在这里），
// 反过来再 import store 就成环。顺带买到的是「不起 SQLite 也测得动收尾」——
// 落库失败不 panic、Finish 幂等这些今天要架一整个网关加一个假上游才验得到。
type Sink func(ctx context.Context, row Row) error

// persistTimeout 是落库的超时。给得短——它只是兜住库卡死，不是给慢查询留余量。
const persistTimeout = 3 * time.Second

// Finish 封盘：记下最终发出去的状态码，落一行 slog 加一行 call_logs。
//
// **顺序是 slog 在前、落库在后**，且两者各自调一次 time.Since——今天
// duration_ms（slog）与 total_ms（库）就是两次调用、相差几微秒。冻结一个 end 让
// 两值恒等是可观测的行为改变，#11 不做；这是现状不是设计。
//
// 幂等：只认第一次。中间件的 defer 保证它恰好被调一次，但 panic 展开路径上多一条
// 保险不亏——重复落库的表现是账翻倍而所有断言照常绿。
func (r *Recorder) Finish(status int) {
	if r.finished {
		return
	}
	r.finished = true
	r.status = status
	// 三档取值到这一刻才定得下来（口径层 v0.74）：中间那档在错误体里，而错误体是边
	// 转发边收的。定一次写回，下面的 slog 与 Row 因此同源，两条转发路径（透传 /
	// 转换）也都只经过这一处——对账与走哪条路无关（v0.56 原则）。
	r.upstreamRequestID = r.resolveUpstreamRequestID()

	// log 与 sink 为 nil 的只有 Detached()：那条流水是给「ctx 里取不到记录」兜底的
	// 黑洞，写进去的东西哪儿也不去，收尾自然也没有出口。
	if r.log == nil {
		return
	}
	// 抱怨排在 call 那一行**之前**：读日志的人先看到「这一行的收场词可能是错的」，
	// 再看那一行本身。放后面的话，扫过去的人多半已经信了它。
	for _, m := range r.misuse {
		r.log.Error("流水收场词用错了半区：回绝与失败是两半，见 calllog.Outcome",
			"call", m, "endpoint", r.endpoint)
	}
	r.log.Info("call", r.LogAttrs()...)
	r.persist()
}

// complain 记下一次「收场词用错了半区」：Refused 收到失败半区的词，或反过来。
//
// 攒着不当场发作，收尾时多打一行 slog.Error。三种处置里选它的理由：
//
//   - **不 panic**：这些动词多数跑在 panic 展开路径上（首字节后断流那条），
//     在这里 panic 会把一次日志分类错误升级成 500，或者打断一条正在输出的流。
//   - **不静默吞**：吞掉就产出一行「看起来挺正常」的流水，而流水的读者没有第二
//     个信源——那正是本次收编要治的病。
//   - 正常路径**零输出**，所以它不改变任何既有行为；重构期它是一张免费的探针，
//     加第 11 个词而忘了给它分半区时也会当场说话。
func (r *Recorder) complain(verb string, o Outcome) {
	r.misuse = append(r.misuse, verb+"("+o.String()+")")
}

// LogAttrs 摊出这一行 slog 的属性。
//
// **顺序即契约**：人眼扫日志靠它，按位置切分的下游（grep/awk 一把梭）也靠它。
// 改动顺序要连 internal/server 的 golden 用例（calllogorder_test.go）一起改，
// 那不是重构是行为变更。
//
// 「有值才打」是这一侧的通例：没发到上游的行每一行都背一个空的 upstream_endpoint、
// 恒为 0 的 retries，读起来只有噪声。库那一侧相反——列是定死的，空值靠 NULL 表达。
func (r *Recorder) LogAttrs() []any {
	attrs := []any{
		"endpoint", r.endpoint,
		"api_key", r.apiKeyName,
		"inbound_protocol", string(r.inboundProto),
		"requested_model", r.requestedModel,
		"stream", r.stream,
		"status", r.status,
		// .String() 不能省：slog 记的必须是 string 而不是 Outcome，
		// 否则属性的动态类型变了，逐字段断言的读者（gatewaytest 的 LogLine.Str）
		// 会集体扑空——而渲染出来的文本一个字都不差，看不出来。
		"outcome", r.outcome.String(),
		"duration_ms", time.Since(r.start).Milliseconds(),
	}
	if r.channel != "" {
		attrs = append(attrs, "channel", r.channel,
			"channel_protocol", string(r.channelProto),
			"upstream_model", r.upstreamModel)
	}
	if r.channelKey != "" {
		attrs = append(attrs, "channel_key", r.channelKey)
	}
	// 出站端点只在有值时进 slog（#20），同 retries / channel_key 那条惯例：没发到上游
	// 的行这一格恒空，每行背一个空字段没意义。**入站那条恒落**（它由中间件写死，
	// 每行都有），两者不对称是因为两者的空值含义不同。打它的理由：channel_protocol
	// 推不出这条路径——同协议透传的 count_tokens 就没有出口对应物，而排障时「转成
	// 了哪条路径」正是跨协议那批要看的第一眼。
	if r.upstreamEndpoint != "" {
		attrs = append(attrs, "upstream_endpoint", r.upstreamEndpoint)
	}
	if r.retries > 0 {
		attrs = append(attrs, "retries", r.retries)
	}
	if r.queueWait > 0 {
		attrs = append(attrs, "queue_wait_ms", r.queueWait.Milliseconds())
	}
	// 与 retries 同理，只在有值时进 slog：上游不回这个头的部署（自建、部分中转）
	// 每一行都背一个空字段没有意义。落库则恒落（列不可空，空串就是「没有」）。
	if r.upstreamRequestID != "" {
		attrs = append(attrs, "upstream_request_id", r.upstreamRequestID)
	}
	if r.haveFirstByte {
		attrs = append(attrs, "ttfb_ms", r.firstByte.Sub(r.start).Milliseconds())
	}
	if r.haveSummary {
		sum := r.summary
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
		// 只在上游真报了这一格时进 slog——没报的时候多一个 reasoning_tokens=0
		// 会被读成「这次没思考」，与流水那一列留 NULL 的理由相同。
		if sum.HasReasoningTokens {
			attrs = append(attrs, "reasoning_tokens", sum.ReasoningTokens)
		}
		if sum.Degraded {
			attrs = append(attrs, "tap_degraded", true)
		}
	}
	if r.requestBody != nil {
		attrs = append(attrs, "request_body", r.requestBody.String())
	}
	if r.responseBody != nil {
		attrs = append(attrs, "response_body", r.responseBody.String())
	}
	return attrs
}

// Row 装配落库的那一行。
//
// 三段可空映射（是不是流式、有没有首字节、有没有 usage）的判据都在这里。
func (r *Recorder) Row() Row {
	row := Row{
		APIKeyName: r.apiKeyName,
		// 端点恒落（#17）：它在 New 那一刻就写死了，鉴权失败、限流、
		// count_tokens 撞非 Anthropic 渠道那些没走到上游的行也都有——那正是最需要
		// 它的一批，那些行的模型、耗时、渠道全是空的，只有端点分得开它们。
		Endpoint: r.endpoint,
		// 出站端点相反，**只有真发过上游的行才有**（#20）：上面那批正是没有它的，
		// 空就是事实，不从 upstream_protocol 反推补一条没打过的路径。
		UpstreamEndpoint: r.upstreamEndpoint,
		ClientProtocol:   string(r.inboundProto),
		UpstreamProtocol: string(r.channelProto),
		ModelRequested:   r.requestedModel,
		ModelUpstream:    r.upstreamModel,
		ChannelName:      r.channel,
		ChannelKeyName:   r.channelKey,
		Status:           r.status,
		RetryCount:       r.retries,
		// 三档都落空、或根本没走到上游时是空串（口径层 v0.56、v0.74）。
		UpstreamRequestID: r.upstreamRequestID,
		TotalMs:           time.Since(r.start).Milliseconds(),
		QueueWaitMs:       r.queueWait.Milliseconds(),
	}
	// stream 是解析请求体那一步才知道的，没走到那一步的行（鉴权失败、限流、body
	// 不是合法 JSON、缺 model）留 NULL——落一个 false 会把「不知道」说成「同步」。
	if r.haveRequest {
		row.IsStream = sql.NullBool{Bool: r.stream, Valid: true}
	}
	// 只记流式（展开层 §7 该列原文「首字节耗时（流式）」）。非流式也填的话它约等于
	// 总耗时，混合流量下「平均首字延迟」就成了一个没有意义的数。非流式的首字节耗时
	// 仍在 slog 的 ttfb_ms 里，没有丢。
	if r.stream && r.haveFirstByte {
		row.TTFTMs = sql.NullInt64{Int64: r.firstByte.Sub(r.start).Milliseconds(), Valid: true}
	}
	// #6 的接缝在这里：Anthropic 渠道的 input 是净值，要加回缓存两项才是流水那一列
	// 的毛值口径（口径层 v0.71）。归一函数由 internal/protocol 暴露，本包只在这里调
	// 一次。**本票不接线**——接了 Anthropic 行的 input_tokens 就会变，与「行为一字
	// 不变」直接冲突。
	if r.haveSummary {
		row.InputTokens = nullInt(r.summary.InputTokens)
		row.OutputTokens = nullInt(r.summary.OutputTokens)
		row.CacheReadTokens = nullInt(r.summary.CacheReadTokens)
		row.CacheWriteTokens = nullInt(r.summary.CacheWriteTokens)
		// 思考 token 单独判（口径层 v0.66）：上面四个是「有 summary 就一定有
		// 数」，这一格不是——上游可能整个 details 容器都不发（老式兼容上游、中转
		// 裁剪、CC 挂非推理模型）。没报就留 NULL，别落 0：落 0 是在说「上游报了，
		// 这次没思考」，而我们其实什么都不知道。
		if r.summary.HasReasoningTokens {
			row.ReasoningTokens = nullInt(r.summary.ReasoningTokens)
		}
	}
	// 表里没有 outcome 列（portage-legacy#22：不动表结构），而「这行为什么不是一次干净的成功」
	// 正是 error 列该承载的。写的是我们自己的固定词表，不是上游原文——上游错误
	// 文案里可能带 base_url。词表与「哪个词落 NULL」见 Outcome.Column。
	row.Error = r.outcome.Column()
	// 上游原文另落一列（口径层 v0.53）。它与 error 列是两件事，也不同步出现：上游
	// 透传 4xx 的 error 列是空的（透传成功不算网关侧错误，v0.28 纪律），detail 却有
	// 值——「可展开」的判据因此是 status >= 400，不是 error 非空。
	// 落库这一刻才截到 2KB：内存里收的是完整的一段（upstreamErrorLimit），因为
	// request_id 取键要在截断前的字节上做（v0.74）。
	if r.errorDetail != nil {
		row.ErrorDetail = sql.NullString{String: r.errorDetail.truncatedTo(errorDetailLimit), Valid: true}
	}
	return row
}

// persist 把这一行写进 call_logs。
//
// 落库失败**不得影响请求**：这里跑的时候响应早已写出去了，改写不了也中断不了，
// 只能记一条 slog error。口径层 §2.5 要的是「每请求一行落库」，但一次 SQLite 抖动
// 不该把一次成功的转发变成客户端眼里的失败。
func (r *Recorder) persist() {
	if r.sink == nil {
		return
	}
	// 不用请求 ctx：客户端断开时它已经 canceled，拿它来写等于「一断线就不记账」，
	// 而被打断恰恰是最需要留痕的时候。
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err := r.sink(ctx, r.Row()); err != nil {
		r.log.Error("调用流水落库失败", "err", err)
	}
}

func nullInt(v int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(v), Valid: true}
}
