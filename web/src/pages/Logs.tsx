import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import type { CallLog, UsageRow } from '../api'
import { Card, CopyCode, Dialog, Empty, ErrorBar, fmtInt, fmtTime, useList } from '../ui'
import { Picker, Segmented } from '../fields'
import type { Option } from '../fields'
import { ModelIcon } from '../icons'

const LOG_FILTERS = [
  { value: 'all' as const, label: '全部' },
  { value: 'bad' as const, label: '只看失败' },
]

/**
 * 端点路径去掉 `/v1/` 前缀。四条路径全都以它开头，摆出来只是给每一行多四个等宽
 * 字符的缩进——真要看全的，title 里挂着原样的那一条。
 */
function shortEndpoint(path: string) {
  return path.startsWith('/v1/') ? path.slice(4) : path
}

/**
 * 端点筛选的选项（#17）。四条转发路径写死在这儿而不是从流水里聚合出来：它们是
 * 常量（protocol.Endpoint 那四个），不像模型名那样跟着配置长——照模型那套按窗口
 * 聚合的话，一个这段时间没被打过的端点就会从下拉里消失，而「这个端点最近一次
 * 是什么时候」恰恰是要它来回答的问题之一。
 *
 * 没有「加列前的老流水」那一档：那些行确实没有这个信息，而本票要分的是
 * count_tokens 与 messages，不是把历史行挑出来看。
 *
 * label 一律由 shortEndpoint 算，跟表里那一列同源，不手抄第二遍。
 */
const ENDPOINT_OPTIONS: Option<string>[] = [
  { value: '', label: '全部端点' },
  ...(
    [
      ['/v1/messages', 'anthropic'],
      ['/v1/messages/count_tokens', 'anthropic'],
      ['/v1/chat/completions', 'openai'],
      ['/v1/responses', 'openai_responses'],
    ] as const
  ).map(([value, hint]) => ({ value, label: shortEndpoint(value), hint })),
]

/**
 * 一页显示多少条流水。它同时是页码的分母（总页数 = 总数 / 它），改这个数就是改页数。
 *
 * 10 而不是 50（PO 2026-08-13 裁定）。这张表有 9 列，且时间之外几乎每格都还带一行
 * 副信息（上游模型、上游协议、首字耗时、缓存读写、重试与错误），一行的实际高度是
 * 普通表格行的两倍多——50 行拉出来是好几屏，而这一页问的是「刚才那几次怎么样」，
 * 答案基本都在最上面几行里。翻回去的代价只是一次请求。
 *
 * 后端不用跟着改：默认 100、上限 500（internal/admin/handlers.go），一页要多少由
 * 这里说了算。
 */
const LOG_PAGE = 10

/**
 * 模型下拉的选项取最近多少天出现过的模型。
 *
 * 这一页没有天数开关：流水靠游标能一直往回翻，天数在这里没有意义（拆页前它是跟
 * 概览共用的那个开关，跟着走而已）。选项列表总得有个窗口，取放得最宽的那档——
 * 窗口越窄，越容易出现「想筛的那个模型不在列表里」。
 */
const MODEL_OPTION_DAYS = 30

function fmtMs(ms: number | null) {
  if (ms === null) return '—'
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(2)}s`
}

/** 这次落到哪个渠道的哪个上游模型。抽出来是为了让截断用的 title 与显示的正文
 *  一定是同一个串——两处各拼一次，早晚会分叉成「看到的」和「悬停看到的」不一样。 */
function upstreamOf(l: CallLog) {
  return l.channel_name ? `${l.channel_name} / ${l.model_upstream}` : '—'
}

/**
 * 这一行的额外细节：弹框，不是展开在表里的第二行（PO 2026-08-13 裁定）。
 *
 * 展开式的毛病是它动的是**表本身**：点开一行，它下面的所有行连同翻页按钮一起往下
 * 掉几百像素，想比对的那几行就跑出了视线；收起来又弹回去。原文是一段要通读的自由
 * 文本，读它的时候本来也不需要同时看着表。
 *
 * 顶上带一小块这次调用的关键信息：框一浮起来就跟那一行脱开了，光一段 JSON 没法回答
 * 「这是刚才点的哪一条」。
 *
 * 里面每一段各带各的判据（PO 2026-08-14 裁定，#81）：这个框答的是「这一行还有什么
 * 可看的」，上游原文只是它目前的内容之一。**上游 request-id 成功行也有**，而上游
 * 原文只在失败行有——把原文那段照挂在成功行上，人会读到「没有存下上游原文」那句
 * 兜底文案，而对一次 200 来说那句是假的。
 */
function LogDetail({ log, onClose }: { log: CallLog; onClose: () => void }) {
  return (
    <Dialog title={`调用详情：${fmtTime(log.created_at)}`} onClose={onClose} wide>
      <div className="form">
        <dl className="log-meta">
          <dt>模型</dt>
          <dd>
            <code>{log.model_requested}</code>
            <span className="muted"> → {upstreamOf(log)}</span>
          </dd>
          {/* 端点与协议在这儿分成两格（#17）：表里那一列为了宽度把入站协议顶掉了，
              框里没有这个约束，而「打的哪条路径」与「哪个协议转成哪个协议」是两个
              问题。端点那格摆入站与出站两条真实路径（#20），与表里那一列同源；
              没有 base_url，那是硬约束。老流水两条都没有，整格塌成一个「—」而不是
              「— → —」——协议在下面那格里还在，摆两个破折号只是把缺值说了两遍。 */}
          <dt>端点</dt>
          <dd className="mono">
            {log.endpoint ? (
              <>
                {log.endpoint} →{' '}
                {log.upstream_endpoint || <span className="muted">未发到上游</span>}
              </>
            ) : (
              <span className="muted">—</span>
            )}
          </dd>
          <dt>协议</dt>
          <dd className="mono">
            {log.client_protocol} → {log.upstream_protocol || '—'}
          </dd>
          <dt>状态</dt>
          <dd>
            <span className={'pill ' + (log.status >= 400 ? 'pill-bad' : 'pill-ok')}>
              {log.status}
            </span>
            {log.retry_count > 0 && <span className="muted"> 重试 ×{log.retry_count}</span>}
            {log.error && <span className="is-bad"> {log.error}</span>}
          </dd>
          {/* 排队耗时（口径层 v0.52，露出见 #7）：只在真排过队时摆。这一列不可空、
              每行必落，0 是「没排队」与「排了 0ms」的同档缺省——照挂在所有行上会让
              人读出「这次排了 0ms」，而绝大多数行的实情是压根没进闸。挂在状态之后：
              queue_full / queue_timeout 两个错误词就落在上面那格，这一格答的是
              「等了多久」。 */}
          {log.queue_wait_ms > 0 && (
            <>
              <dt>排队</dt>
              <dd className="tnum">{fmtMs(log.queue_wait_ms)}</dd>
            </>
          )}
          {/* 上游 request-id（口径层 v0.56，露出见 #81）：走 CopyCode 而不是裸 code——
              它的唯一用途是粘给上游查这一次调用，`req_018Ee…` 这种串手抄必错。
              空串不摆空壳：三种情况（没走到上游、上游没回这个头、v0.56 之前的老流水）
              在对账上是同一件事，一律「—」，与其余几格的缺值同语义。 */}
          <dt>上游 request-id</dt>
          <dd>
            {log.upstream_request_id ? (
              <CopyCode value={log.upstream_request_id} title="点击复制，拿去上游对账" />
            ) : (
              <span className="muted">—</span>
            )}
          </dd>
        </dl>
        {/* 上游原文原样摊开，不解析不美化：它是不可控文本，我们对它唯一的加工是截到 2KB。
            null 与空串分开说——「没存」与「上游一个字都没回」是两条不同的线索。
            只在失败行摆：成功行没有这一段，而下面那三句兜底文案对着一次 200 全是假话。 */}
        {log.status >= 400 && (
          <pre className={log.error_detail ? 'log-detail' : 'log-detail muted'}>
            {log.error_detail === null
              ? '这一行没有存下上游原文（v0.53 之前的老流水，或失败发生在拿到响应体之前）。'
              : log.error_detail === ''
                ? '上游没有返回任何响应体。'
                : log.error_detail}
          </pre>
        )}
      </div>
    </Dialog>
  )
}

/**
 * 流水的取数：筛选下推后端，翻页是「钉住窗口上沿 + 在窗口里按页偏移」。
 *
 * 两个参数缺一不可（口径层 v0.61）。光给 offset 会错位——流水是时间序、新行不断插到
 * 头部，翻到第二页时 offset 已经被新写入的行推着往后错，第一页的末几行会再出现一遍。
 * 光给游标（v0.53 那版）跳不了页——`before` 没有逆向形式，也没有「往前数 60 条」这种
 * 形式，能回答的只有「来路」与「下一批」，所以那一版只能一页一页地挪。
 *
 * 钉法是进页时记下**第一发拿到的最大 id**（`anchor`），此后每一发都带 `before=anchor+1`
 * （before 取的是严格小于）。总数由后端按同一组条件、同一个 before 数出来，于是翻页
 * 途中新写进来的流水既挤不进任何一页，也不会让总页数在手底下变。代价是这期间新来的
 * 调用要按一下「刷新」才看得到——而那正是刷新这颗按钮的意思。
 *
 * anchor 放 ref 不放 state：它只在请求里当参数用，改了不需要重渲染；放 state 的话
 * 「钉住」这个动作会多触发一轮渲染，而那一轮里 rows 还是旧的。
 *
 * 不用 useList：它只管「拉一次、整块换掉」，装不下页码与 anchor 这两样。
 *
 * 筛选下推后端而不是在前端过滤：筛选和分页搅在一起时，本页里过滤只会筛出
 * 「这一页里的失败」，而人问的是「这段时间的失败」。
 */
function useLogFeed(only: string, model: string, endpoint: string) {
  const [rows, setRows] = useState<CallLog[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(0)
  // 窗口上沿。0 = 还没钉住（进页第一发、刚换筛选、刚刷新）。
  const anchor = useRef(0)

  const load = useCallback(
    async (p: number) => {
      const q = new URLSearchParams({
        limit: String(LOG_PAGE),
        offset: String(p * LOG_PAGE),
      })
      if (only === 'bad') q.set('only', 'bad')
      if (model) q.set('model', model)
      if (endpoint) q.set('endpoint', endpoint)
      if (anchor.current) q.set('before', String(anchor.current + 1))
      setLoading(true)
      try {
        const res = await api.get<{ rows: CallLog[] | null; total: number }>(`/logs?${q}`)
        const got = res.rows ?? []
        // 第一发落地才知道上沿在哪。空结果不钉——库里一条都没有时钉个 0 没有意义，
        // 下一发照样从最新算起。
        if (!anchor.current && got.length) anchor.current = got[0].id
        setRows(got)
        setTotal(res.total)
        setError('')
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [only, model, endpoint],
  )

  // 翻页只有这一个入口：页码与请求必须同时改。分开写的话，某条路径漏改页码就会
  // 出现「高亮停在第 3 页、内容却是第 1 页」，而这种错只在下一次点击时才暴露。
  const go = useCallback(
    (p: number) => {
      setPage(p)
      void load(p)
    },
    [load],
  )

  // 筛选一变回到第一页并松开上沿：留在第 3 页上换条件，那个偏移量指的是另一批数据里
  // 的位置；上沿也得重钉，否则换完条件仍按上一批的最大 id 截着。
  useEffect(() => {
    anchor.current = 0
    go(0)
  }, [go])

  return {
    rows,
    error,
    loading,
    page,
    // 总页数至少 1：没有流水时也是「第 1 页，空的」，算成 0 页的话页码那一排会整个消失，
    // 而消失与「只有一页」在视觉上是同一件事，没必要分。
    pages: Math.max(1, Math.ceil(total / LOG_PAGE)),
    go,
    // 刷新 = 重钉上沿并回第一页：流水是时间序，「刷新」问的是最新那批，而它只可能在第一页。
    reload: () => {
      anchor.current = 0
      go(0)
    },
  }
}

/**
 * 页码那一排摆哪几个数。
 *
 * 首末两页恒在（「一共几页」是这排里唯一一个不点也要读的数），当前页左右各留一个，
 * 中间断开处摆省略号。总页数少的时候全摆——省略号换不来宽度反而多一个符号。
 */
const PAGE_WINDOW = 5

function pageList(cur: number, count: number): (number | 'gap')[] {
  if (count <= PAGE_WINDOW + 2) return Array.from({ length: count }, (_, i) => i)
  // 中间那段固定 PAGE_WINDOW 个，跟着当前页走但不越过首末两页——不夹住的话，翻到
  // 最后几页时这排会先缩短再变长，宽度一路抖。
  const start = Math.max(1, Math.min(cur - 2, count - 1 - PAGE_WINDOW))
  const end = start + PAGE_WINDOW
  const out: (number | 'gap')[] = [0]
  // 只隔着一页时把那一页摆出来：省略号跟一个页码差不多宽，替掉一页是白换。
  if (start === 2) out.push(1)
  else if (start > 1) out.push('gap')
  for (let i = start; i < end; i++) out.push(i)
  if (end === count - 2) out.push(end)
  else if (end < count - 1) out.push('gap')
  out.push(count - 1)
  return out
}

/**
 * 调用记录：**刚才那几次怎么样**（口径层 v0.60 三分之一）。
 *
 * 排障是即时的，所以这一页不带统计窗口、不带聚合——那两件事分别在概览页与排行页。
 */
export default function Logs() {
  const [filter, setFilter] = useState('all')
  const [model, setModel] = useState('')
  // 按端点筛（#17）：`/v1/messages` 与 `/v1/messages/count_tokens` 的入站协议同为
  // anthropic，不给这个筛子，一批 count_tokens 的 501 与真正的转发失败在表里长得一样。
  const [endpoint, setEndpoint] = useState('')
  // 正在弹框里看的那一条（上游原文 v0.53、上游 request-id #81）。存整行而不是 id：翻页和刷新
  // 会把 rows 整块换掉，存 id 的话框还开着、内容却已经查无此行。
  const [detail, setDetail] = useState<CallLog | null>(null)
  // 模型下拉的选项问的是「这段时间出现过哪些模型」，与排行页那份按维度聚合的数据
  // 是两个问题，所以自己拉一次、固定按模型维度。
  const models = useList(
    () => api.get<{ rows: UsageRow[] | null }>(`/usage?days=${MODEL_OPTION_DAYS}&by=model`),
    [],
  )
  const logs = useLogFeed(filter, model, endpoint)

  const shown = logs.rows

  // 模型下拉的选项。第一项是「不筛」——它是一个取值（空串），不是 placeholder，
  // 所以得摆进列表里，否则选了别的之后没有路退回来。
  //
  // 选中的模型可能不在列表里：30 天窗口之外的老流水仍能翻到，翻到时那个模型早已
  // 不在列表里，而 model 这个筛选条件还在生效。补一项回去并注明——不补的话触发器
  // 显示的是「全部模型」，而表里明明还按它筛着。
  const modelOptions = useMemo<Option<string>[]>(() => {
    const seen = models.data?.rows ?? []
    const opts: Option<string>[] = [{ value: '', label: '全部模型' }]
    if (model && !seen.some((r) => r.label === model)) {
      opts.push({
        value: model,
        label: model,
        hint: '这段时间没有',
        icon: <ModelIcon model={model} size={16} />,
      })
    }
    for (const r of seen) {
      opts.push({ value: r.label, label: r.label, icon: <ModelIcon model={r.label} size={16} /> })
    }
    return opts
  }, [models.data, model])

  return (
    <>
      <ErrorBar message={logs.error} />
      <Card
        title="调用记录"
        action={
          <div className="row-actions">
            {/* 模型筛选与「只看失败」都下推后端（v0.53）：在已拉回的那一页里过滤，
                筛出的是「这一页里的失败」，而人问的是「这段时间的失败」。
                控件走 Picker 不走原生 select：这一格要带厂商图标、模型多了要能搜，
                `<option>` 里塞不进元素；原生弹层的选中态还是系统蓝，与 DESIGN §3
                「accent 只给焦点环」是两套颜色语言。 */}
            <div className="picker-inline" title="按请求的模型名筛选">
              <Picker
                value={model}
                options={modelOptions}
                onChange={setModel}
                placeholder="全部模型"
              />
            </div>
            {/* 端点筛选另起一个 Picker，不并进 Segmented：连上「全部」一共 5 段，
                横排铺开比模型下拉还宽，而这一排右边还有「只看失败」和「刷新」。 */}
            <div className="picker-inline" title="按转发端点筛选">
              <Picker
                value={endpoint}
                options={ENDPOINT_OPTIONS}
                onChange={setEndpoint}
                placeholder="全部端点"
              />
            </div>
            <Segmented value={filter} options={LOG_FILTERS} onChange={setFilter} />
            <button className="btn btn-quiet" onClick={logs.reload}>
              刷新
            </button>
          </div>
        }
      >
        {/* 表装在一个**定高**的视口里（PO 2026-08-13 裁定）。
            行高本来就不齐——失败行在「状态」那格里多挂重试、错误词和详情按钮，比普通
            行高出三十来像素；末页还常常不满十行。不定高的话，每翻一页整块表和它下面
            的翻页按钮就换一个高度，手要跟着按钮跑。定高之后按钮钉死在一个位置，超出
            的部分在视口内部滚，页面本身不动。
            空态也放在同一个视口里：摆在外面的话，筛成空的一瞬间这块区域塌掉，正是要
            消掉的那种跳动。 */}
        <div className="log-viewport">
          {shown.length === 0 ? (
            <Empty>
              {logs.loading
                ? '读取中…'
                : filter === 'bad'
                  ? '这些条件下没有失败。'
                  : '还没有流水。'}
            </Empty>
          ) : (
            <table className="table table-plain table-logs">
              <thead>
                <tr>
                  <th>时间</th>
                  {/* 「API Key」是界面上的正名（CONTEXT.md）：叫「API 密钥」会与
                      渠道页那段「上游凭证」同形不同义，而这一列指的是网关下发的那把。 */}
                  <th>API Key</th>
                  <th>模型</th>
                  <th>端点</th>
                  <th>类型</th>
                  <th>上游凭证</th>
                  <th className="num">状态</th>
                  <th className="num">耗时</th>
                  <th className="num">in / out</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((l) => (
                  <tr key={l.id}>
                    <td className="nowrap">{fmtTime(l.created_at)}</td>
                    {/* 哪把网关 key 打的（v0.53 以前压在时间下面，独立成列后筛查更顺眼） */}
                    <td className="nowrap">
                      {l.api_key_name || <span className="muted">—</span>}
                    </td>
                    {/* 请求的模型是主信息，落到哪个渠道的哪个上游模型是次信息，压在下面一行 */}
                    <td className="log-model">
                      <span className="icon-row nowrap">
                        <ModelIcon model={l.model_requested} size={16} />
                        <code title={l.model_requested}>{l.model_requested}</code>
                      </span>
                      <div className="sub" title={upstreamOf(l)}>
                        {upstreamOf(l)}
                      </div>
                    </td>
                    {/* 端点：主行是真端点路径（#17），副行是上游协议，恒排两行。
                        入站协议不再单摆一行（PO 2026-08-17 裁定）——它由路径完全
                        决定（入口协议由路径定，不嗅探请求体），两个都摆是同一件事
                        说两遍；而路径能答的那个问题（这行是 messages 还是
                        count_tokens）入站协议答不了，两者的 client_protocol 同为
                        anthropic。
                        副行由「上游 <协议>」换成**出站路径**（#20，PO 2026-08-17
                        裁定）：协议推不出路径——count_tokens 透传时上游协议是
                        anthropic，照它反推就把 count_tokens 说成了 messages。协议
                        不再摆在这一列，要看去详情框。只摆路径不摆 base_url，那是
                        硬约束。
                        两档回落各有各的意思，靠入站那列分辨：**两列皆空**是加列前
                        的老流水，退回原来的「入站 / 上游 <协议>」两行——那些行确实
                        没存过路径，摆「—」等于把已知的协议也扔了；**入站有、出站
                        空**说明这次请求根本没发出去（401 / 429 / 501 / 队满），副行
                        给个「—」。 */}
                    <td className="nowrap">
                      {l.endpoint ? (
                        <>
                          <span className="route" title={l.endpoint}>
                            <span className="route-node">{shortEndpoint(l.endpoint)}</span>
                          </span>
                          <div className="sub">
                            <span className="route" title={l.upstream_endpoint || undefined}>
                              <span className="muted">→</span>
                              {l.upstream_endpoint ? (
                                <span className="route-node">
                                  {shortEndpoint(l.upstream_endpoint)}
                                </span>
                              ) : (
                                '—'
                              )}
                            </span>
                          </div>
                        </>
                      ) : (
                        <>
                          <span className="route">
                            <span className="muted">入站</span>
                            <span className="route-node">{l.client_protocol}</span>
                          </span>
                          <div className="sub">
                            <span className="route">
                              <span className="muted">上游</span>
                              {l.upstream_protocol ? (
                                <span className="route-node">{l.upstream_protocol}</span>
                              ) : (
                                '—'
                              )}
                            </span>
                          </div>
                        </>
                      )}
                    </td>
                    {/* 同步/流式。null 是「不知道」：没解析到请求体的行（鉴权失败
                        那类）与加列前的老流水——写成「同步」是撒谎。 */}
                    <td className="nowrap">
                      {l.is_stream === null ? (
                        <span className="muted">—</span>
                      ) : l.is_stream ? (
                        '流式'
                      ) : (
                        '同步'
                      )}
                    </td>
                    {/* 上游凭证（口径层 v0.38）：多凭证放开后，「这次是哪个号在跑」
                        是排障第一问。记的是最后真正发出请求的那一份。 */}
                    <td className="nowrap">
                      {l.channel_key_name ? l.channel_key_name : <span className="muted">—</span>}
                    </td>
                    {/* 错误压在状态下面，不单占一列：它写的是网关自己的**固定词表**
                        （stream_aborted / unauthorized / rejected…，见 server/calllog.go），
                        短且可枚举，为它留一整列会把表推得比卡片还宽，右边直接看不见。 */}
                    <td className="num">
                      <span className={'pill ' + (l.status >= 400 ? 'pill-bad' : 'pill-ok')}>
                        {l.status}
                      </span>
                      {l.retry_count > 0 && <div className="sub">重试 ×{l.retry_count}</div>}
                      {l.error && (
                        <div className="sub log-err" title={l.error}>
                          {l.error}
                        </div>
                      )}
                      {/* 判据是「这一行有没有细节可看」（PO 2026-08-14 裁定，#81），
                          不是 status >= 400 那一条。
                          失败这一半的判据是状态码而不是 error 非空：上游透传 4xx 的
                          error 列本就是空的（透传成功不算网关侧错误），而那正是最想
                          点开看上游到底说了什么的一种行。
                          request-id 这一半把成功行也带进来了——用量对不上、想找上游
                          查这次到底计了多少时，要的正是成功行的那个 id。它不另占一列
                          （二十几个字符会把表撑出卡片）也不压在时间下面（同理），而
                          这个框本来就是「这一行的额外细节」，上游原文只是它此前唯一
                          的内容。
                          排队耗时这一半同理（#7）：真排过队（> 0）的行多一段可看，
                          成功与否无关——挤过闸的成功行正是看拥塞时要找的那种。 */}
                      {(l.status >= 400 || l.upstream_request_id !== '' || l.queue_wait_ms > 0) && (
                        <button
                          type="button"
                          className="btn btn-ghost log-detail-toggle"
                          onClick={() => setDetail(l)}
                        >
                          详情
                        </button>
                      )}
                    </td>
                    <td className="num nowrap tnum">
                      {fmtMs(l.total_ms)}
                      <div className="sub">
                        {l.ttft_ms === null ? '—' : `首字 ${fmtMs(l.ttft_ms)}`}
                      </div>
                    </td>
                    {/* 缓存读/写只在非零时露出：绝大多数上游根本不报这两个数，
                        每行挂一对 0 是纯噪声，而 Anthropic 那条链路上它是成本的大头。 */}
                    <td className="num nowrap tnum muted">
                      {fmtInt(l.input_tokens)} / {fmtInt(l.output_tokens)}
                      {/* 三元而非 &&：两个都是 0 时 `0 && <div/>` 会把那个 0 渲染出来 */}
                      {l.cache_read_tokens || l.cache_write_tokens ? (
                        <div className="sub">
                          缓存 读 {fmtInt(l.cache_read_tokens)} / 写{' '}
                          {fmtInt(l.cache_write_tokens)}
                        </div>
                      ) : null}
                      {/* 思考 token 同款只在有数时露出（口径层 v0.66）。判据是
                          `> 0` 而不是非 null：null（上游不报）与 0（报了、这次没
                          思考）在库里分得开，但对着一行流水，两者都没有可看的成本，
                          多一行「思考 0」是噪声。它是输出 token 的明细，不另计。 */}
                      {l.reasoning_tokens ? (
                        <div className="sub">思考 {fmtInt(l.reasoning_tokens)}</div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        {/* 页码摆在表的**右下角**，并且点得动（PO 2026-08-13 裁定：「分页按钮放在右下角，
            可以直接跳转那种」，推翻 DESIGN v0.13 ② 的后半）。
            v0.13 当时拒的是「摆一排 1 2 3 却点不动中间那些」——那是纯游标翻页留下的
            残缺。现在窗口上沿由 anchor 钉住、页内位置交给 offset（见 useLogFeed），
            页码是真能跳的，总页数也是真的，那条理由不再成立。
            超过一页才出现：只有一页时摆一排翻不动的按钮是噪音。 */}
        {logs.pages > 1 && (
          <div className="pager">
            <button
              className="btn btn-quiet"
              disabled={logs.page === 0 || logs.loading}
              onClick={() => logs.go(logs.page - 1)}
            >
              上一页
            </button>
            {pageList(logs.page, logs.pages).map((it, i) =>
              it === 'gap' ? (
                // 省略号不是按钮：中间断掉的那几页没有一个「代表页」，做成能点的会
                // 让人以为点了会去某一页，而去哪一页说不清。
                <span key={`gap-${i}`} className="pager-gap" aria-hidden="true">
                  …
                </span>
              ) : (
                <button
                  key={it}
                  // 当前页复用 btn-quiet 的选中态（底色 + 文字色描边），不另开一抹颜色：
                  // accent 只给焦点环（DESIGN §3）。
                  className={'btn btn-quiet pager-page tnum' + (it === logs.page ? ' is-on' : '')}
                  aria-current={it === logs.page ? 'page' : undefined}
                  disabled={logs.loading}
                  onClick={() => logs.go(it)}
                >
                  {it + 1}
                </button>
              ),
            )}
            <button
              className="btn btn-quiet"
              disabled={logs.page >= logs.pages - 1 || logs.loading}
              onClick={() => logs.go(logs.page + 1)}
            >
              下一页
            </button>
          </div>
        )}
      </Card>
      {detail && <LogDetail log={detail} onClose={() => setDetail(null)} />}
    </>
  )
}
