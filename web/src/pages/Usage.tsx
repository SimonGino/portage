import { useState } from 'react'
import { api } from '../api'
import type { CallLog, UsageRow } from '../api'
import { Card, Empty, ErrorBar, fmtInt, fmtTime, useList } from '../ui'
import { ModelIcon } from '../icons'

const DAY_OPTIONS = [1, 7, 30]

export default function Usage() {
  const [days, setDays] = useState(7)
  const usage = useList(
    () => api.get<{ days: number; rows: UsageRow[] | null }>(`/usage?days=${days}`),
    [days], // 天数一变就重拉
  )
  const logs = useList(() => api.get<CallLog[] | null>('/logs?limit=100'))

  const rows = usage.data?.rows ?? []
  const list = logs.data ?? []

  return (
    <>
      <ErrorBar message={usage.error || logs.error} />
      <Card
        title="用量"
        action={
          <div className="row-actions">
            {DAY_OPTIONS.map((d) => (
              <button
                key={d}
                className={'btn btn-quiet' + (d === days ? ' is-on' : '')}
                onClick={() => setDays(d)}
              >
                {d} 天
              </button>
            ))}
          </div>
        }
      >
        {rows.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>模型</th>
                <th className="num">调用</th>
                <th className="num">失败</th>
                <th className="num">输入 token</th>
                <th className="num">输出 token</th>
                <th className="num">缓存读</th>
                <th className="num">缓存写</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.model_requested}>
                  <td>
                    <span className="icon-row">
                      <ModelIcon model={r.model_requested} size={16} />
                      <code>{r.model_requested}</code>
                    </span>
                  </td>
                  <td className="num">{fmtInt(r.calls)}</td>
                  <td className={'num' + (r.errors > 0 ? ' is-bad' : '')}>{fmtInt(r.errors)}</td>
                  <td className="num">{fmtInt(r.input_tokens)}</td>
                  <td className="num">{fmtInt(r.output_tokens)}</td>
                  <td className="num">{fmtInt(r.cache_read_tokens)}</td>
                  <td className="num">{fmtInt(r.cache_write_tokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <Card
        title="最近调用"
        action={
          <button className="btn btn-quiet" onClick={() => void logs.reload()}>
            刷新
          </button>
        }
      >
        {list.length === 0 ? (
          <Empty>还没有流水。</Empty>
        ) : (
          <div className="scroll-x">
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>key</th>
                  <th>模型</th>
                  <th>渠道 / 上游模型</th>
                  <th>链路</th>
                  <th className="num">状态</th>
                  <th className="num">首字</th>
                  <th className="num">总耗时</th>
                  <th className="num">in/out</th>
                  <th>错误</th>
                </tr>
              </thead>
              <tbody>
                {list.map((l) => (
                  <tr key={l.id}>
                    <td className="nowrap muted">{fmtTime(l.created_at)}</td>
                    <td className="nowrap">{l.api_key_name || <span className="muted">—</span>}</td>
                    <td className="nowrap">
                      <span className="icon-row">
                        <ModelIcon model={l.model_requested} size={16} />
                        <code>{l.model_requested}</code>
                      </span>
                    </td>
                    <td className="muted nowrap">
                      {l.channel_name ? `${l.channel_name} / ${l.model_upstream}` : '—'}
                    </td>
                    {/* 客户端协议 → 上游协议：一眼看出这一次走没走转换 */}
                    <td className="muted nowrap">
                      {l.client_protocol}
                      {l.upstream_protocol && l.upstream_protocol !== l.client_protocol
                        ? ` → ${l.upstream_protocol}`
                        : ''}
                    </td>
                    <td className={'num' + (l.status >= 400 ? ' is-bad' : '')}>
                      {l.status}
                      {l.retry_count > 0 && <span className="muted"> ×{l.retry_count + 1}</span>}
                    </td>
                    <td className="num muted">{l.ttft_ms === null ? '—' : `${l.ttft_ms}ms`}</td>
                    <td className="num muted">{l.total_ms}ms</td>
                    <td className="num muted nowrap">
                      {fmtInt(l.input_tokens)} / {fmtInt(l.output_tokens)}
                    </td>
                    <td className="err">{l.error}</td>
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
