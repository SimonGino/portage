import RankingsView from '../rankings/view'
import { QuotaCard, type QuotaHook } from './quota'

/**
 * 「用量与配额」页（DESIGN §12，v0.53 改排行形态）：本月配额进度置顶，其下整个
 * 是排行页那条下钻链的本人版（节律带 → 构成 → 谁在烧，rankings/view.tsx 的 mine
 * 形态）。此前的聚合表与简版流水（v0.48）退役：聚合表被排行列表顶掉（同一份数据
 * 画两遍），流水独立成「调用记录」tab（logs/view.tsx 的 mine 形态）。
 */
export default function MyUsage({ quota }: { quota: QuotaHook }) {
  return (
    <>
      <QuotaCard quota={quota.data} />
      <RankingsView mine />
    </>
  )
}
