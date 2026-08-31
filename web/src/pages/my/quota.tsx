import { api } from '../../api'
import type { QuotaState } from '../../api'
import { fmtMoney, useList } from '../../ui'

/** 本人配额（GET /my/quota）。壳拉一份给顶栏微条，页面各自再拉自己的（要新鲜数）。 */
export function useQuota() {
  return useList(() => api.get<QuotaState>('/my/quota'))
}

export type QuotaHook = ReturnType<typeof useQuota>

/**
 * 顶栏配额微条（DESIGN §12）：一眼看「这个月还能不能用」。不限额时只摆本月已用
 * ——没有分母就不画进度，画一根永远空的条是在暗示「还有很多」。
 */
export function QuotaChip({ quota }: { quota: QuotaState | null }) {
  if (!quota) return null
  const limit = quota.monthly_quota_usd
  if (limit === null) {
    return <span className="quota-chip">本月 {fmtMoney(quota.spent_usd)}</span>
  }
  if (limit === 0) {
    return <span className="quota-chip is-over">已封停</span>
  }
  const over = quota.spent_usd >= limit
  return (
    <span
      className={'quota-chip' + (over ? ' is-over' : '')}
      title={`本月已用 ${fmtMoney(quota.spent_usd)} / 限额 ${fmtMoney(limit)}`}
    >
      {fmtMoney(quota.spent_usd)} / {fmtMoney(limit)}
    </span>
  )
}

/**
 * 配额卡（DESIGN §12：配额永远置顶）。三态：不限额只报已用；有限额画进度并在
 * 用尽时说清「下月自动恢复」；0 = 封停，直说。
 */
export function QuotaCard({ quota }: { quota: QuotaState | null }) {
  if (!quota) return null
  const limit = quota.monthly_quota_usd
  const spent = quota.spent_usd
  if (limit === 0) {
    return (
      <div className="quota-card">
        <div className="bar bar-error">配额为 0：该账号的转发已封停，请联系管理员。</div>
      </div>
    )
  }
  if (limit === null) {
    return (
      <div className="quota-card">
        <div className="quota-line">
          <span className="quota-num">{fmtMoney(spent)}</span>
          <span className="muted">本月已用 · 不限额</span>
        </div>
      </div>
    )
  }
  const ratio = Math.min(spent / limit, 1)
  const over = spent >= limit
  return (
    <div className="quota-card">
      <div className="quota-line">
        <span className="quota-num">{fmtMoney(spent)}</span>
        <span className="muted">
          / 本月限额 {fmtMoney(limit)}（UTC 自然月，下月自动恢复）
        </span>
      </div>
      <div className="quota-track" role="progressbar" aria-valuenow={Math.round(ratio * 100)}>
        <div className={'quota-fill' + (over ? ' is-over' : '')} style={{ width: `${ratio * 100}%` }} />
      </div>
      {over && <div className="bar bar-warn">本月配额已用尽，转发请求会被拒到下月；调额请联系管理员。</div>}
    </div>
  )
}
