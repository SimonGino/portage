import { Fragment, useCallback, useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import type { CallLog, UsageRow } from '../api'
import { Card, Empty, ErrorBar, fmtInt, fmtTime, useList } from '../ui'
import { Segmented } from '../fields'
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

/** 一次拉多少条流水。翻页靠 before 游标，不是 offset——见 store.CallLogFilter。 */
const LOG_PAGE = 50

/** 大数缩写成 12.3k / 4.5M：指标条上要的是量级，精确值在下面的明细表里。 */
function fmtCompact(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k'
  return (n / 1_000_000).toFixed(1) + 'M'
}

const tokensOf = (r: UsageRow) => r.input_tokens + r.output_tokens

/** 一根柱子最多画到第几名。再多柱子就细到读不出来，剩下的在下面那张表里。 */
const CHART_TOP = 8

/**
 * 堆叠柱状图：一根柱子一行，下段输入、上段输出，高度按 token 总量相对最大值。
 *
 * 只在有 token 可画时出现。绝大多数上游会报 usage，但 sub2api 这类中转有时整段
 * 不报——那时全部行都是 0，画出来是一排贴地的横线，不如不画：一张说不出话的图
 * 比没有图更浪费那 200px。
 */
function UsageChart({ rows }: { rows: UsageRow[] }) {
  const top = [...rows].sort((a, b) => tokensOf(b) - tokensOf(a)).slice(0, CHART_TOP)
  const max = Math.max(...top.map(tokensOf), 0)
  if (max === 0) return null
  // 占比的分母取**全部**行，不是画出来的这几根：截断是显示上的事，「占了多少」
  // 问的是在总量里的份额，拿前 8 名当全集会把每个百分比都抬高。
  const totalTokens = rows.reduce((a, r) => a + tokensOf(r), 0) || 1

  return (
    <>
      <div className="usage-chart">
        {top.map((r) => {
          const outShare = tokensOf(r) ? (r.output_tokens / tokensOf(r)) * 100 : 0
          return (
            <div className="usage-col" key={r.label}>
              {/* 0.6% 是给「有调用但一个 token 都没报」的行留的一线，让它在图上
                  仍然占一格位置——直接高度 0 的话，那一行会从图里凭空消失，而
                  下面的表里明明有它。 */}
              <div
                className="usage-col-stack"
                style={{ height: `${Math.max((tokensOf(r) / max) * 100, 0.6)}%` }}
                title={`${r.label}：输入 ${fmtInt(r.input_tokens)} · 输出 ${fmtInt(r.output_tokens)}`}
              >
                <div className="usage-seg-out" style={{ height: `${outShare}%` }} />
                <div className="usage-seg-in" style={{ height: `${100 - outShare}%` }} />
              </div>
              <div className="usage-col-label" title={r.label}>
                {r.label}
              </div>
            </div>
          )
        })}
      </div>
      {/* 图例 + 一句「该看什么」（DESIGN.md §6）。堆叠的两段没法直接标注在柱子上
          （细柱塞不下两个数），这正是 §6 允许「系列区分不开时才用色」的那种情况；
          但光有图例就落进 §8 那条「图例代替直接标注」，所以把结论写出来——一张图
          该说的是「谁是大头」，不是「这里有两种颜色」。 */}
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
          <code>{top[0].label}</code> 占了 {((tokensOf(top[0]) / totalTokens) * 100).toFixed(1)}%
        </span>
        {rows.length > CHART_TOP && <span>只画了前 {CHART_TOP} 个，其余在下表</span>}
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
 * 流水的取数：筛选下推后端、翻页用 before 游标增量追加。
 *
 * 不用 useList：它每次都整块换掉 data，而这里要的是「在已有的后面接一段」。筛选变了
 * 才从头拉——筛选和分页搅在一起时，前端在本页里过滤只会筛出「这一页里的失败」，
 * 而人问的是「这段时间的失败」。
 */
function useLogFeed(only: string, model: string) {
  const [rows, setRows] = useState<CallLog[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  // more 只在「上一次正好拉满一页」时为真。少于一页说明后面没有了，不必再多问一次
  // 才发现是空的。
  const [more, setMore] = useState(false)

  const load = useCallback(
    async (before?: number) => {
      const q = new URLSearchParams({ limit: String(LOG_PAGE) })
      if (only === 'bad') q.set('only', 'bad')
      if (model) q.set('model', model)
      if (before) q.set('before', String(before))
      setLoading(true)
      try {
        const page = (await api.get<CallLog[] | null>(`/logs?${q}`)) ?? []
        setRows((prev) => (before ? [...prev, ...page] : page))
        setMore(page.length === LOG_PAGE)
        setError('')
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [only, model],
  )

  useEffect(() => {
    void load()
  }, [load])

  const loadMore = () => {
    const last = rows[rows.length - 1]
    if (last) void load(last.id)
  }
  return { rows, error, loading, more, reload: () => void load(), loadMore }
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
  // 模型下拉的选项单独按模型维度拉一次：它要的是「这段时间出现过哪些模型」，
  // 与上面那份按当前维度聚合的数据是两个问题，维度切到 API Key 时不该跟着变空。
  const models = useList(
    () => api.get<{ rows: UsageRow[] | null }>(`/usage?days=${days}&by=model`),
    [days],
  )
  const logs = useLogFeed(filter, model)

  const rows = usage.data?.rows ?? []
  const shown = logs.rows

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
      <ErrorBar message={usage.error || logs.error} />

      <Card
        title="用量"
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

        <UsageChart rows={rows} />

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
        title="最近调用"
        action={
          <div className="row-actions">
            {/* 模型筛选与「只看失败」都下推后端（v0.53）：在已拉回的那一页里过滤，
                筛出的是「这一页里的失败」，而人问的是「这段时间的失败」。 */}
            <select
              className="input select-inline"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              title="按请求的模型名筛选"
            >
              <option value="">全部模型</option>
              {(models.data?.rows ?? []).map((r) => (
                <option key={r.label} value={r.label}>
                  {r.label}
                </option>
              ))}
            </select>
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
                  <th>模型</th>
                  <th>链路</th>
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
                    <td className="nowrap">
                      {fmtTime(l.created_at)}
                      <div className="sub">{l.api_key_name || '—'}</div>
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
                    {/* 客户端协议 → 上游协议：转换过的才出现第二枚芯片 */}
                    <td>
                      <span className="route">
                        <span className="route-node">{l.client_protocol}</span>
                        {l.upstream_protocol && l.upstream_protocol !== l.client_protocol && (
                          <>
                            <span className="route-arrow">→</span>
                            <span className="route-node">{l.upstream_protocol}</span>
                          </>
                        )}
                      </span>
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
                        <td colSpan={7}>
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
        {/* 「加载更多」而不是页码：流水是时间序，新行不断插到头部，页码翻到第二页
            时早就错位了（后端为此走 before 游标）。 */}
        {logs.more && (
          <div className="row-actions load-more">
            <button className="btn btn-quiet" disabled={logs.loading} onClick={logs.loadMore}>
              {logs.loading ? '加载中…' : '加载更多'}
            </button>
          </div>
        )}
      </Card>
    </>
  )
}
