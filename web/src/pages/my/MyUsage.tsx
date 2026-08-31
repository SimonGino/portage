import { useState } from 'react'
import { api } from '../../api'
import type { CallLog, UsageRow } from '../../api'
import { Card, Dialog, Empty, ErrorBar, fmtInt, fmtMoney, fmtMoneyDetail, fmtTime, useList } from '../../ui'
import { Segmented } from '../../fields'
import { ModelIcon } from '../../icons'
import { QuotaCard, type QuotaHook } from './quota'

/**
 * 「用量与配额」页（DESIGN §12）：本月配额进度置顶 + 按模型/按 key 聚合 + 本人
 * 流水。流水含渠道名与错误详情（自查「我这次为什么失败」），没有凭证名与上游
 * request-id——那是运营细节，服务端出口就裁掉了。金额标准档两位小数、明细四位。
 */
export default function MyUsage({ quota }: { quota: QuotaHook }) {
  return (
    <>
      <QuotaCard quota={quota.data} />
      <UsageSection />
      <LogsSection />
    </>
  )
}

function UsageSection() {
  const [by, setBy] = useState<'model' | 'key'>('model')
  const usage = useList(
    () => api.get<{ rows: UsageRow[] | null }>(`/my/usage?days=30&by=${by}`),
    [by],
  )
  const rows = usage.data?.rows ?? []

  return (
    <Card
      title="近 30 天用量"
      action={
        <Segmented
          value={by}
          options={[
            { value: 'model', label: '按模型' },
            { value: 'key', label: '按 Key' },
          ]}
          onChange={setBy}
        />
      }
    >
      <ErrorBar message={usage.error} />
      {rows.length === 0 ? (
        <Empty>这 30 天还没有调用。</Empty>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>{by === 'model' ? '模型' : 'API Key'}</th>
              <th className="tnum">调用</th>
              <th className="tnum">失败</th>
              <th className="tnum">输入 token</th>
              <th className="tnum">输出 token</th>
              <th className="tnum">费用</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.label}>
                <td>
                  <span className="chip">
                    <ModelIcon model={r.label} size={14} />
                    <code>{r.label}</code>
                  </span>
                </td>
                <td className="tnum">{fmtInt(r.calls)}</td>
                <td className="tnum">{r.errors > 0 ? fmtInt(r.errors) : <span className="muted">0</span>}</td>
                <td className="tnum">{fmtInt(r.input_tokens)}</td>
                <td className="tnum">{fmtInt(r.output_tokens)}</td>
                <td className="tnum">{fmtMoney(r.cost_usd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  )
}

function LogsSection() {
  const [onlyBad, setOnlyBad] = useState(false)
  const logs = useList(
    () =>
      api.get<{ rows: CallLog[] | null; total: number }>(
        `/my/logs?limit=50${onlyBad ? '&only=bad' : ''}`,
      ),
    [onlyBad],
  )
  const [open, setOpen] = useState<CallLog | null>(null)
  const rows = logs.data?.rows ?? []

  return (
    <Card
      title="我的调用"
      action={
        <Segmented
          value={onlyBad ? 'bad' : 'all'}
          options={[
            { value: 'all', label: '全部' },
            { value: 'bad', label: '只看失败' },
          ]}
          onChange={(v) => setOnlyBad(v === 'bad')}
        />
      }
    >
      <ErrorBar message={logs.error} />
      {rows.length === 0 ? (
        <Empty>{onlyBad ? '没有失败的调用。' : '还没有调用记录。'}</Empty>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>时间</th>
              <th>模型</th>
              <th>Key</th>
              <th className="tnum">状态</th>
              <th className="tnum">输入</th>
              <th className="tnum">输出</th>
              <th className="tnum">费用</th>
              <th className="col-actions" />
            </tr>
          </thead>
          <tbody>
            {rows.map((l) => (
              <tr key={l.id}>
                <td className="muted">{fmtTime(l.created_at)}</td>
                <td>
                  <span className="chip">
                    <ModelIcon model={l.model_requested} size={14} />
                    <code>{l.model_requested || '—'}</code>
                  </span>
                </td>
                <td className="muted">{l.api_key_name}</td>
                <td className="tnum">
                  <span className={'pill ' + (l.status >= 400 ? 'pill-bad' : 'pill-ok')}>{l.status}</span>
                </td>
                <td className="tnum">{fmtInt(l.input_tokens)}</td>
                <td className="tnum">{fmtInt(l.output_tokens)}</td>
                <td className="tnum">{l.cost === null ? <span className="muted">—</span> : fmtMoney(l.cost)}</td>
                {/* 有细节可看才给按钮：失败原因，或这一笔的四位小数账目（判据同 Logs 页，
                    去掉了用户侧看不到的 request-id 与排队两项）。 */}
                <td className="col-actions">
                  {(l.status >= 400 || l.cost !== null) && (
                    <button type="button" className="btn btn-ghost" onClick={() => setOpen(l)}>
                      详情
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {(logs.data?.total ?? 0) > rows.length && (
        <p className="muted">
          只显示最近 {rows.length} 条，共 {logs.data?.total} 条。
        </p>
      )}
      {open && <MyLogDetail log={open} onClose={() => setOpen(null)} />}
    </Card>
  )
}

/**
 * 本人流水详情。与管理端 Logs 的详情框同形制但少两格：凭证名与上游 request-id
 * 服务端就没发（#75 出口裁剪），这里不摆空壳。渠道名与错误原文照摆——自查
 * 「我这次为什么失败」正要它们。
 */
function MyLogDetail({ log, onClose }: { log: CallLog; onClose: () => void }) {
  return (
    <Dialog title={`调用详情：${fmtTime(log.created_at)}`} onClose={onClose} wide>
      <div className="form">
        <dl className="log-meta">
          <dt>模型</dt>
          <dd>
            <code>{log.model_requested}</code>
            {log.model_upstream && log.model_upstream !== log.model_requested && (
              <span className="muted"> → {log.model_upstream}</span>
            )}
          </dd>
          {log.channel_name && (
            <>
              <dt>渠道</dt>
              <dd>{log.channel_name}</dd>
            </>
          )}
          <dt>状态</dt>
          <dd>
            <span className={'pill ' + (log.status >= 400 ? 'pill-bad' : 'pill-ok')}>{log.status}</span>
            {log.retry_count > 0 && <span className="muted"> 重试 ×{log.retry_count}</span>}
            {log.error && <span className="is-bad"> {log.error}</span>}
          </dd>
          {log.cost !== null && (
            <>
              <dt>成本</dt>
              <dd className="tnum">{fmtMoneyDetail(log.cost)}</dd>
            </>
          )}
        </dl>
        {log.status >= 400 && log.error_detail !== null && (
          <pre className="bar bar-error bar-pre">{log.error_detail || '（上游回了错误但响应体是空的）'}</pre>
        )}
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </Dialog>
  )
}
