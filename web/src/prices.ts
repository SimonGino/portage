/**
 * 单价显示的共用件（口径层 §2.10 / v1.10）：渠道详情页的定价胶囊、定价页总表、
 * 「我的」模型页三处同一副读法，别各抄一遍再各自漂移。
 */

/** 单价显示：USD/百万 token 的定价惯用形（$3、$0.3、$3.75），不是金额展示的
 *  `$X.XX`——那条管的是算出来的钱，单价抹成两位会把 $0.075 写成 $0.08。 */
export function fmtPrice(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  return '$' + String(n)
}

/** 四价各自的短标签，编辑胶囊与 title 共用一份，别两处各抄一遍。 */
export const PRICE_FIELDS = [
  ['input', '入'],
  ['output', '出'],
  ['cache_read', '缓读'],
  ['cache_write', '缓写'],
] as const
