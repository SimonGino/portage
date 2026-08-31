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

/** 四价的统一形状：纳管条目（price_input…）与建议价（input…）字段名不同，
 *  消费端先收成这个再进下面两个判定，三处页面不各拼各的。 */
export type FourPrices = Partial<Record<(typeof PRICE_FIELDS)[number][0], number | null>>

/** 未定价判据的「四价全空」半边：null 与 undefined 都算空（建议价用可选字段）。 */
export function isUnpriced(p: FourPrices): boolean {
  return PRICE_FIELDS.every(([k]) => p[k] === null || p[k] === undefined)
}

/** 四价全文，进 title 用：「入 $3，出 $15，缓读 —，缓写 —。USD/百万 token」。 */
export function fmtFourTitle(p: FourPrices): string {
  return PRICE_FIELDS.map(([k, label]) => `${label} ${fmtPrice(p[k])}`).join('，') + '。USD/百万 token'
}
