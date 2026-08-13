import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import type { CallLog, DailyUsage, UsageRow } from '../api'
import { Card, Empty, ErrorBar, fmtInt, fmtTime, useList } from '../ui'
import { Picker, Segmented } from '../fields'
import type { Option } from '../fields'
import { ModelIcon } from '../icons'

const DAY_OPTIONS = [
  { value: '1' as const, label: '1 天' },
  { value: '7' as const, label: '7 天' },
  { value: '30' as const, label: '30 天' },
]

// 聚合维度（v0.38 加了上游凭证，v0.53 加了 API Key）。
//
// 「按上游凭证」写全称：它聚合的是渠道下那些上游 key 的名字，跟你在这个网关里新建的
// API Key 是两回事，只写「按凭证」两边都像。
const DIM_OPTIONS = [
  { value: 'model' as const, label: '按模型' },
  { value: 'key' as const, label: '按 API Key' },
  { value: 'credential' as const, label: '按上游凭证' },
]

/** 一行行数按维度换个量词，别让「3 个模型」和「3 份凭证」长成同一句。 */
const DIM_UNIT: Record<string, string> = { model: '个模型', key: '把 API Key', credential: '份上游凭证' }

const LOG_FILTERS = [
  { value: 'all' as const, label: '全部' },
  { value: 'bad' as const, label: '只看失败' },
]

/** 一页显示多少条流水。翻页靠 before 游标，不是 offset——见 store.CallLogFilter。 */
const LOG_PAGE = 50

/** 大数缩写成 12.3k / 4.5M：指标条上要的是量级，精确值在下面的明细表里。 */
function fmtCompact(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k'
  return (n / 1_000_000).toFixed(1) + 'M'
}

const tokensOfDay = (d: DailyUsage) => d.input_tokens + d.output_tokens

/** 横轴最多摆几个日期标签。30 根柱子每根都标，横轴会糊成一条黑线。 */
const AXIS_LABELS = 8

/** 8/13 这种短日期：横轴上要回答的是「哪天」，年份与前导零都是噪音。 */
function fmtDay(day: string) {
  const [, m, d] = day.split('-')
  return `${Number(m)}/${Number(d)}`
}

/**
 * 按天的堆叠柱状图：一根柱子一天，下段输入、上段输出，高度按 token 总量相对最大值。
 *
 * 横轴是**时间**而不是模型（DESIGN v0.14）：按模型排名的那一版画的是「谁在烧」，
 * 而那正是它正下方那张表逐行说过的事——图只是把同一份数据又画了一遍，还少了失败数
 * 与缓存两列。时间是这一页别处答不了的问题：什么时候烧的、在涨还是在落。
 *
 * 只在有 token 可画时出现。绝大多数上游会报 usage，但 sub2api 这类中转有时整段
 * 不报——那时全部天都是 0，画出来是一排贴地的横线，不如不画：一张说不出话的图
 * 比没有图更浪费那 200px。
 */
function UsageChart({ days }: { days: DailyUsage[] }) {
  const max = Math.max(...days.map(tokensOfDay), 0)
  if (max === 0) return null
  const total = days.reduce((a, d) => a + tokensOfDay(d), 0) || 1
  const peak = days.reduce((a, d) => (tokensOfDay(d) > tokensOfDay(a) ? d : a), days[0])
  const step = Math.ceil(days.length / AXIS_LABELS)

  return (
    <>
      <div className="usage-chart">
        {days.map((d, i) => {
          const t = tokensOfDay(d)
          const outShare = t ? (d.output_tokens / t) * 100 : 0
          return (
            <div className="usage-col" key={d.day}>
              {/* 0.6% 是给「这天有调用但一个 token 都没报」留的一线，让它仍占一格
                  位置。没有调用的那天则是真的 0——那天空着是实话，不该也画一条。 */}
              <div
                className="usage-col-stack"
                style={{ height: `${Math.max((t / max) * 100, d.calls > 0 ? 0.6 : 0)}%` }}
                title={`${d.day}：${fmtInt(d.calls)} 次调用 · 输入 ${fmtInt(d.input_tokens)} · 输出 ${fmtInt(d.output_tokens)}`}
              >
                <div className="usage-seg-out" style={{ height: `${outShare}%` }} />
                <div className="usage-seg-in" style={{ height: `${100 - outShare}%` }} />
              </div>
              {/* 标签隔着摆，柱子照样一根不少：横轴密到读不出来时，该少的是标签。 */}
              <div className="usage-col-label" title={d.day}>
                {i % step === 0 || i === days.length - 1 ? fmtDay(d.day) : ''}
              </div>
            </div>
          )
        })}
      </div>
      {/* 图例 + 一句「该看什么」（DESIGN.md §6）。堆叠的两段没法直接标注在柱子上
          （细柱塞不下两个数），这正是 §6 允许「系列区分不开时才用色」的那种情况；
          但光有图例就落进 §8 那条「图例代替直接标注」，所以把结论写出来——一张图
          该说的是「哪天最重」，不是「这里有两种颜色」。 */}
      <div className="usage-legend">
        <span>
          <i className="usage-dot" style={{ background: 'var(--data-in)' }} />
          输入
        </span>
        <span>
          <i className="usage-dot" style={{ background: 'var(--data-out)' }} />
          输出
        </span>
        <span>
          最重的一天是 <code>{fmtDay(peak.day)}</code>，占这 {days.length} 天的{' '}
          {((tokensOfDay(peak) / total) * 100).toFixed(1)}%
        </span>
        {/* 说出来，否则每次看最后一根都比前一根矮，会被读成「在掉」。 */}
        <span className="muted">最后一根是今天，还没走完</span>
      </div>
    </>
  )
}

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
 * 流水的取数：筛选下推后端、翻页走 before 游标栈。
 *
 * 不用 useList：它只管「拉一次、整块换掉」，装不下这个游标栈。栈是必须的——before
 * 游标没有逆向形式，「上一页从哪开始」算不出来，只能是来时记下的那一个，所以往下
 * 翻一页就把当前页末行的 id 压栈，回退靠出栈。
 *
 * 每次多要一条（LOG_PAGE + 1）只为回答「还有没有下一页」，多的那条不显示：拿
 * 「这一页正好拉满」当判据的话，总行数恰好是整页倍数时「下一页」会翻进一页空表。
 *
 * 筛选下推后端而不是在前端过滤：筛选和分页搅在一起时，本页里过滤只会筛出
 * 「这一页里的失败」，而人问的是「这段时间的失败」。
 */
function useLogFeed(only: string, model: string) {
  const [rows, setRows] = useState<CallLog[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [more, setMore] = useState(false)
  // 走到当前这一页所用的游标序列。栈深就是当前页的 0-based 页码，空栈 = 第一页。
  const [stack, setStack] = useState<number[]>([])

  const load = useCallback(
    async (before?: number) => {
      const q = new URLSearchParams({ limit: String(LOG_PAGE + 1) })
      if (only === 'bad') q.set('only', 'bad')
      if (model) q.set('model', model)
      if (before) q.set('before', String(before))
      setLoading(true)
      try {
        const page = (await api.get<CallLog[] | null>(`/logs?${q}`)) ?? []
        setRows(page.slice(0, LOG_PAGE))
        setMore(page.length > LOG_PAGE)
        setError('')
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [only, model],
  )

  // 翻页只有这一个入口：页码与请求必须同时改。分开写的话，某条路径漏改栈就会
  // 出现「页码停在第 3 页、内容却是第 1 页」，而这种错只在按下一页时才暴露。
  const go = useCallback(
    (next: number[]) => {
      setStack(next)
      void load(next[next.length - 1])
    },
    [load],
  )

  // 筛选一变回到第一页：留在第 3 页上换条件，手里那个游标指的是另一批数据里的位置。
  useEffect(() => {
    go([])
  }, [go])

  return {
    rows,
    error,
    loading,
    more,
    page: stack.length,
    // 下一页的起点取**显示出来**的末行，不是多要的那一条——拿探路那行当游标会把它跳过。
    next: () => {
      const last = rows[rows.length - 1]
      if (last) go([...stack, last.id])
    },
    prev: () => go(stack.slice(0, -1)),
    // 刷新回第一页：流水是时间序，「刷新」问的是最新那批，而它只可能在第一页。
    reload: () => go([]),
  }
}

export default function Usage() {
  const [days, setDays] = useState('7')
  const [dim, setDim] = useState('model')
  const [filter, setFilter] = useState('all')
  const [model, setModel] = useState('')
  // 展开看上游原文的那些行（口径层 v0.53）。默认全收起：这一列是不可控的上游文本，
  // 摊开在表里会把每一行撑成一屏。
  const [opened, setOpened] = useState<number[]>([])
  const usage = useList(
    () => api.get<{ days: number; rows: UsageRow[] | null }>(`/usage?days=${days}&by=${dim}`),
    [days, dim], // 天数或维度一变就重拉
  )
  // 图只跟天数有关，与聚合维度无关，所以单独一个端点、单独 keyed 在 days 上——
  // 挂在上面那份聚合里的话，每切一次维度都要把它重算一遍。
  const daily = useList(
    () => api.get<{ days: number; rows: DailyUsage[] | null }>(`/usage/daily?days=${days}`),
    [days],
  )
  // 模型下拉的选项单独按模型维度拉一次：它要的是「这段时间出现过哪些模型」，
  // 与上面那份按当前维度聚合的数据是两个问题，维度切到 API Key 时不该跟着变空。
  const models = useList(
    () => api.get<{ rows: UsageRow[] | null }>(`/usage?days=${days}&by=model`),
    [days],
  )
  const logs = useLogFeed(filter, model)

  const rows = usage.data?.rows ?? []
  const shown = logs.rows

  // 模型下拉的选项。第一项是「不筛」——它是一个取值（空串），不是 placeholder，
  // 所以得摆进列表里，否则选了别的之后没有路退回来。
  //
  // 选中的模型可能不在列表里：天数一改，这段时间没出现过它，它就从列表里消失了，
  // 而 model 这个筛选条件还在生效。补一项回去并注明——不补的话触发器显示的是
  // 「全部模型」，而表里明明还按它筛着。
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

  // 指标条是这几行的合计，后端没有单独的汇总接口，前端加一遍就够——行数是模型数量级。
  const total = useMemo(
    () =>
      rows.reduce(
        (a, r) => ({
          calls: a.calls + r.calls,
          errors: a.errors + r.errors,
          input: a.input + r.input_tokens,
          output: a.output + r.output_tokens,
          cacheRead: a.cacheRead + r.cache_read_tokens,
          cacheWrite: a.cacheWrite + r.cache_write_tokens,
        }),
        { calls: 0, errors: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      ),
    [rows],
  )
  const errRate = total.calls ? (total.errors / total.calls) * 100 : 0

  return (
    <>
      <ErrorBar message={usage.error || daily.error || logs.error} />

      <Card
        title="概览"
        action={
          <div className="row-actions">
            <Segmented value={dim} options={DIM_OPTIONS} onChange={setDim} />
            <Segmented value={days} options={DAY_OPTIONS} onChange={setDays} />
          </div>
        }
      >
        {/* 一条有主次的指标行，不是三张等大卡（DESIGN.md §8，v0.11 照参照图做过
            那一版，PO 没选）。调用是主语，失败次之且只有非零才上错误色，token 合计
            退成右边的附注——拆开的输入/输出在下面的图与表里各有一份。 */}
        <div className="stats">
          <div className="stat-lead">
            <span className="stat-lead-value">{fmtInt(total.calls)}</span>
            <span className="stat-lead-label">
              次调用 · {rows.length} {DIM_UNIT[dim] ?? '个模型'}
            </span>
          </div>
          <div className={'stat-side' + (total.errors > 0 ? ' is-bad' : '')}>
            失败 <b>{fmtInt(total.errors)}</b>
            <span>{total.calls ? `${errRate.toFixed(1)}%` : '—'}</span>
          </div>
          {/* 只留一个合计。输入/输出拆开的那两项已经由下面的柱状图和它的图例
              说了一遍，这里再说一遍就是把同一个数说两遍——图上分不出量级的
              精确值本来就该去表里看。 */}
          <div className="stat-tokens">
            <span>
              合计 <b>{fmtCompact(total.input + total.output)}</b> token
              {total.cacheRead > 0 && `（缓存读 ${fmtCompact(total.cacheRead)}）`}
            </span>
          </div>
        </div>

        <UsageChart days={daily.data?.rows ?? []} />

        {rows.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <div className="scroll-x">
            <table className="table table-plain table-usage">
              <thead>
                <tr>
                  <th>{DIM_OPTIONS.find((o) => o.value === dim)?.label.slice(1) ?? '模型'}</th>
                  <th className="num">调用</th>
                  <th className="num">失败</th>
                  <th className="num">输入</th>
                  <th className="num">输出</th>
                  <th className="num">缓存读</th>
                  <th className="num">缓存写</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.label}>
                    <td className="model-cell">
                      <span className="icon-row">
                        {/* 只有模型维度画图标：那个图标是从模型名猜厂商猜出来的，
                            套在人自己起的凭证名/key 名上只会猜出一堆无意义的首字母块。 */}
                        {dim === 'model' ? <ModelIcon model={r.label} size={16} /> : null}
                        <code>{r.label}</code>
                      </span>
                    </td>
                    <td className="num">{fmtInt(r.calls)}</td>
                    <td className="num">
                      {r.errors > 0 ? (
                        <span className="pill pill-bad">{fmtInt(r.errors)}</span>
                      ) : (
                        <span className="muted">0</span>
                      )}
                    </td>
                    <td className="num muted">{fmtInt(r.input_tokens)}</td>
                    <td className="num muted">{fmtInt(r.output_tokens)}</td>
                    <td className="num muted">{fmtInt(r.cache_read_tokens)}</td>
                    <td className="num muted">{fmtInt(r.cache_write_tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

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
              <Picker value={model} options={modelOptions} onChange={setModel} placeholder="全部模型" />
            </div>
            <Segmented value={filter} options={LOG_FILTERS} onChange={setFilter} />
            <button className="btn btn-quiet" onClick={logs.reload}>
              刷新
            </button>
          </div>
        }
      >
        {shown.length === 0 ? (
          <Empty>
            {logs.loading
              ? '读取中…'
              : filter === 'bad'
                ? '这些条件下没有失败。'
                : '还没有流水。'}
          </Empty>
        ) : (
          <div className="scroll-x">
            <table className="table table-plain table-logs">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>API 密钥</th>
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
                  <Fragment key={l.id}>
                    <tr>
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
                    {/* 端点：入站/上游各占一行，恒排两行——原「链路」的箭头式单行
                        （同协议折叠成一枚芯片）省了宽度，代价是两枚芯片哪边是哪边
                        全靠箭头方向猜。上游没走到时是「—」，与模型列的 sub 同语义。 */}
                    <td className="nowrap">
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
                      {/* 判据是状态码，不是 error 非空：上游透传 4xx 的 error 列
                          本就是空的（透传成功不算网关侧错误），而那正是最想点开
                          看上游到底说了什么的一种行。 */}
                      {l.status >= 400 && (
                        <button
                          type="button"
                          className="btn btn-ghost log-detail-toggle"
                          onClick={() =>
                            setOpened((prev) =>
                              prev.includes(l.id)
                                ? prev.filter((id) => id !== l.id)
                                : [...prev, l.id],
                            )
                          }
                        >
                          {opened.includes(l.id) ? '收起' : '详情'}
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
                    </td>
                    </tr>
                    {opened.includes(l.id) && (
                      <tr className="log-detail-row">
                        <td colSpan={9}>
                          {/* 上游原文原样摊开，不解析不美化：它是不可控文本，
                              我们对它唯一的加工是截到 2KB。
                              null 与空串分开说——「没存」与「上游一个字都没回」是两条不同的线索。 */}
                          <pre className={l.error_detail ? 'log-detail' : 'log-detail muted'}>
                            {l.error_detail === null
                              ? '这一行没有存下上游原文（v0.53 之前的老流水，或失败发生在拿到响应体之前）。'
                              : l.error_detail === ''
                                ? '上游没有返回任何响应体。'
                                : l.error_detail}
                          </pre>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {/* 页码，不是「加载更多」（PO 2026-08-13 裁定，推翻 DESIGN v0.12 那一版）。
            只有上一页/下一页与「第几页」，没有「跳到第 5 页」也没有总页数：后端走
            before 游标（流水是时间序、新行不断插到头部，offset 翻页会让同一条在两页
            里各出现一次），游标能回答的只有「来路」和「下一批」。
            一页装满才出现——只有一页时摆一排翻不动的按钮是噪音。 */}
        {(logs.page > 0 || logs.more) && (
          <div className="row-actions pager">
            <button
              className="btn btn-quiet"
              disabled={logs.page === 0 || logs.loading}
              onClick={logs.prev}
            >
              上一页
            </button>
            <span className="pager-at tnum">第 {logs.page + 1} 页</span>
            <button
              className="btn btn-quiet"
              disabled={!logs.more || logs.loading}
              onClick={logs.next}
            >
              下一页
            </button>
          </div>
        )}
      </Card>
    </>
  )
}
