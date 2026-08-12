import { useMemo, useState } from 'react'
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

// 聚合维度（口径层 v0.38）：按凭证是「这个号跑了多少」的答案，只给逐行的流水表
// 等于把 group by 留给人的肉眼做。
const DIM_OPTIONS = [
  { value: 'model' as const, label: '按模型' },
  { value: 'credential' as const, label: '按凭证' },
]

const LOG_FILTERS = [
  { value: 'all' as const, label: '全部' },
  { value: 'bad' as const, label: '只看失败' },
]

/** 大数缩写成 12.3k / 4.5M：指标条上要的是量级，精确值在下面的明细表里。 */
function fmtCompact(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k'
  return (n / 1_000_000).toFixed(1) + 'M'
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

export default function Usage() {
  const [days, setDays] = useState('7')
  const [dim, setDim] = useState('model')
  const [filter, setFilter] = useState('all')
  const usage = useList(
    () => api.get<{ days: number; rows: UsageRow[] | null }>(`/usage?days=${days}&by=${dim}`),
    [days, dim], // 天数或维度一变就重拉
  )
  const logs = useList(() => api.get<CallLog[] | null>('/logs?limit=100'))

  const rows = usage.data?.rows ?? []
  const list = logs.data ?? []
  const shown = filter === 'bad' ? list.filter((l) => l.status >= 400) : list

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
  const maxCalls = Math.max(1, ...rows.map((r) => r.calls))
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
        {/* 一条有主次的指标行，不是四个等大方块（DESIGN.md §8）。调用是主语，失败
            次之且只有非零才上错误色，两个 token 数退成右边的附注——逐模型的明细就在
            下面那张表里，顶上再摆一遍只是把同一个数说两遍。 */}
        <div className="stats">
          <div className="stat-lead">
            <span className="stat-lead-value">{fmtInt(total.calls)}</span>
            <span className="stat-lead-label">
              次调用 · {rows.length} {dim === 'credential' ? '份凭证' : '个模型'}
            </span>
          </div>
          <div className={'stat-side' + (total.errors > 0 ? ' is-bad' : '')}>
            失败 <b>{fmtInt(total.errors)}</b>
            <span>{total.calls ? `${errRate.toFixed(1)}%` : '—'}</span>
          </div>
          <div className="stat-tokens">
            <span>
              输入 <b>{fmtCompact(total.input)}</b>
              {total.cacheRead > 0 && `（缓存读 ${fmtCompact(total.cacheRead)}）`}
            </span>
            <span>
              输出 <b>{fmtCompact(total.output)}</b>
              {total.cacheWrite > 0 && `（缓存写 ${fmtCompact(total.cacheWrite)}）`}
            </span>
          </div>
        </div>

        {rows.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <div className="scroll-x">
            <table className="table table-plain table-usage">
              <thead>
                <tr>
                  <th>{dim === 'credential' ? '上游凭证' : '模型'}</th>
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
                        {/* 凭证维度不画模型图标：那个图标是从模型名猜厂商猜出来的，
                            套在人自己起的凭证名上只会猜出一堆无意义的首字母块。 */}
                        {dim === 'credential' ? null : <ModelIcon model={r.label} size={16} />}
                        <code>{r.label}</code>
                      </span>
                      <div
                        className="bar-mini"
                        style={{ width: `${(r.calls / maxCalls) * 100}%` }}
                      />
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
            <Segmented value={filter} options={LOG_FILTERS} onChange={setFilter} />
            <button className="btn btn-quiet" onClick={() => void logs.reload()}>
              刷新
            </button>
          </div>
        }
      >
        {shown.length === 0 ? (
          <Empty>{filter === 'bad' && list.length > 0 ? '这一百条里没有失败。' : '还没有流水。'}</Empty>
        ) : (
          <div className="scroll-x">
            <table className="table table-plain table-logs">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>模型</th>
                  <th>链路</th>
                  <th>凭证</th>
                  <th className="num">状态</th>
                  <th className="num">耗时</th>
                  <th className="num">in / out</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((l) => (
                  <tr key={l.id}>
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
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  )
}
