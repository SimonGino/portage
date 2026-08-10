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
        <div className="stats">
          <div className="stat">
            <div className="stat-label">调用</div>
            <div className="stat-value">{fmtInt(total.calls)}</div>
            <div className="stat-sub">
              {rows.length} {dim === 'credential' ? '份凭证' : '个模型'}
            </div>
          </div>
          <div className="stat">
            <div className="stat-label">失败</div>
            <div className={'stat-value' + (total.errors > 0 ? ' is-bad' : '')}>
              {fmtInt(total.errors)}
            </div>
            <div className="stat-sub">{total.calls ? `${errRate.toFixed(1)}%` : '—'}</div>
          </div>
          <div className="stat">
            <div className="stat-label">输入 token</div>
            <div className="stat-value">{fmtCompact(total.input)}</div>
            <div className="stat-sub">缓存读 {fmtCompact(total.cacheRead)}</div>
          </div>
          <div className="stat">
            <div className="stat-label">输出 token</div>
            <div className="stat-value">{fmtCompact(total.output)}</div>
            <div className="stat-sub">缓存写 {fmtCompact(total.cacheWrite)}</div>
          </div>
        </div>

        {rows.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <div className="scroll-x">
            <table className="table table-plain">
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
            <table className="table table-plain">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>模型</th>
                  <th>链路</th>
                  <th>凭证</th>
                  <th className="num">状态</th>
                  <th className="num">耗时</th>
                  <th className="num">in / out</th>
                  <th>错误</th>
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
                    <td>
                      <span className="icon-row nowrap">
                        <ModelIcon model={l.model_requested} size={16} />
                        <code>{l.model_requested}</code>
                      </span>
                      <div className="sub">
                        {l.channel_name ? `${l.channel_name} / ${l.model_upstream}` : '—'}
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
                    <td className="num">
                      <span className={'pill ' + (l.status >= 400 ? 'pill-bad' : 'pill-ok')}>
                        {l.status}
                      </span>
                      {l.retry_count > 0 && <div className="sub">重试 ×{l.retry_count}</div>}
                    </td>
                    <td className="num nowrap tnum">
                      {fmtMs(l.total_ms)}
                      <div className="sub">
                        {l.ttft_ms === null ? '—' : `首字 ${fmtMs(l.ttft_ms)}`}
                      </div>
                    </td>
                    <td className="num nowrap tnum muted">
                      {fmtInt(l.input_tokens)} / {fmtInt(l.output_tokens)}
                    </td>
                    {/* 错误单行截断，全文进 title：它可能是一整段上游返回的 JSON */}
                    <td>
                      {l.error ? (
                        <span className="log-err" title={l.error}>
                          {l.error}
                        </span>
                      ) : (
                        <span className="muted">—</span>
                      )}
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
