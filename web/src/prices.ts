/**
 * 单价显示的共用件（口径层 §2.10 / v1.10）：渠道详情页的定价胶囊、定价页总表、
 * 「我的」模型页三处同一副读法，别各抄一遍再各自漂移。
 */

import { useEffect, useState } from 'react'
import { api } from './api'
import type { ChannelModel, PricingModelPrice, PricingModels, PricingProvider } from './api'
import type { Option } from './fields'

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

/** 分档四价的键与标签，与 PRICE_FIELDS 一一对应（口径层 v1.49，#185）。 */
export const TIER_FIELDS = [
  ['tier_input', '入'],
  ['tier_output', '出'],
  ['tier_cache_read', '缓读'],
  ['tier_cache_write', '缓写'],
] as const

type PriceKey = (typeof PRICE_FIELDS)[number][0] | (typeof TIER_FIELDS)[number][0] | 'tier_above'

/** 填价那一笔的整组（= PUT /channel-models/:id 的 prices，store.ChannelModelPrices）：
 *  基础四价 + 阈值 + 分档四价。整组覆盖，漏一个键就是把它清成 null。 */
export type PriceBody = Record<PriceKey, number | null>

const PRICE_KEYS: readonly PriceKey[] = [
  ...PRICE_FIELDS.map(([k]) => k),
  'tier_above',
  ...TIER_FIELDS.map(([k]) => k),
]

export function modelPriceBody(m: ChannelModel): PriceBody {
  return {
    input: m.price_input, output: m.price_output, cache_read: m.price_cache_read, cache_write: m.price_cache_write,
    tier_above: m.price_tier_above, tier_input: m.price_tier_input, tier_output: m.price_tier_output,
    tier_cache_read: m.price_tier_cache_read, tier_cache_write: m.price_tier_cache_write,
  }
}

/** 建议价 → 采纳时落的那一组：连分档一起，快照缺的价 null 不补 0。 */
export function suggestPriceBody(s: PricingModelPrice): PriceBody {
  const t = s.tier
  return {
    input: s.input ?? null, output: s.output ?? null, cache_read: s.cache_read ?? null, cache_write: s.cache_write ?? null,
    tier_above: t?.above ?? null, tier_input: t?.input ?? null, tier_output: t?.output ?? null,
    tier_cache_read: t?.cache_read ?? null, tier_cache_write: t?.cache_write ?? null,
  }
}

export function samePrices(a: PriceBody, b: PriceBody): boolean {
  return PRICE_KEYS.every((k) => a[k] === b[k])
}

/** 编辑草稿 → 整组。空 = null；解析不出、负数、阈值不是正整数都整组不存（回 null）——
 *  存下能解析的那几个会把没看清的输入悄悄写成 null。阈值空 = 去分档，分档四价一并清。 */
export function parsePriceDraft(draft: Record<string, string>): PriceBody | null {
  const out = {} as PriceBody
  for (const k of PRICE_KEYS) {
    const raw = (draft[k] ?? '').trim()
    if (raw === '') {
      out[k] = null
      continue
    }
    const n = Number(raw)
    if (!Number.isFinite(n) || n < 0) return null
    if (k === 'tier_above' && !(Number.isInteger(n) && n > 0)) return null
    out[k] = n
  }
  if (out.tier_above === null) for (const [k] of TIER_FIELDS) out[k] = null
  return out
}

/** 阈值简写进胶囊：200000 → 200k、1000000 → 1M；不整千的照原数。 */
export function fmtTokensShort(n: number): string {
  if (n >= 1e6 && n % 1e6 === 0) return `${n / 1e6}M`
  if (n % 1000 === 0) return `${n / 1000}k`
  return String(n)
}

/** 分档全文，进 title：「超过 200000 token 整笔按：入 $6，出 $22.5，缓读 沿用基础价…」。 */
export function fmtTierTitle(b: PriceBody): string {
  return (
    `超过 ${b.tier_above} token 整笔按：` +
    TIER_FIELDS.map(([k, label]) => `${label} ${b[k] === null ? '沿用基础价' : fmtPrice(b[k])}`).join('，')
  )
}

/*
 * 厂商标注与 models.dev 建议价（口径层 §2.10，#74）的取法也收在这里（#143）：
 * 设置表单、模型页身份条、定价页三处以前各拉各的，「快照名单外」规则已经分叉。
 * 名单是发版内置的只读资产，进程里拉一次全局共用；失败要不要挂错误条由调用方定
 * ——标注是可选项，多数地方不挂。
 */

let providersOnce: Promise<PricingProvider[]> | null = null

/** models.dev 的 provider 名单。拉到之前是空数组；拉失败 error 非空且下次挂载重拉。 */
export function useProviders(): { list: PricingProvider[]; error: string } {
  const [list, setList] = useState<PricingProvider[]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    let gone = false
    providersOnce ??= api.get<PricingProvider[]>('/pricing/providers')
    providersOnce
      .then((l) => {
        if (!gone) setList(l)
      })
      .catch((e: unknown) => {
        providersOnce = null
        if (!gone) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      gone = true
    }
  }, [])
  return { list, error }
}

/** 标注选择器的选项：「未标注」打头；库里存的标注可能不在快照名单里（自由文本，快照随
 *  发版才更新），补一项回去并注明——不补的话触发器显示「未标注」，而库里明明有值。 */
export function providerOptions(list: PricingProvider[], current: string): Option<string>[] {
  const opts: Option<string>[] = [{ value: '', label: '未标注' }]
  if (current && !list.some((p) => p.id === current)) {
    opts.push({ value: current, label: current, hint: '快照名单外' })
  }
  for (const p of list) opts.push({ value: p.id, label: p.name, hint: p.id })
  return opts
}

/** provider id → 人话名（302ai → 302 AI）；名单里没有（或还没拉到）就摆 id 本身。 */
export function providerName(list: PricingProvider[], id: string): string {
  return list.find((p) => p.id === id)?.name ?? id
}

/** 各 provider 的建议价快照：provider id → 模型名 → 四价。空 id 不拉；拉失败当没有
 *  建议（快照是发版内置资产，失败多半是版本不齐）。只做填表助手——建议不落库、
 *  不参与计价，人点「采纳」写进去的才算数。 */
export function useSuggested(providers: readonly string[]): Record<string, Record<string, PricingModelPrice>> {
  const [map, setMap] = useState<Record<string, Record<string, PricingModelPrice>>>({})
  // 按内容比较：调用方多半每次渲染都是新数组。
  const key = JSON.stringify(providers)
  useEffect(() => {
    let gone = false
    for (const p of JSON.parse(key) as string[]) {
      if (!p) continue
      api
        .get<PricingModels>(`/pricing/models?provider=${encodeURIComponent(p)}`)
        .then((r) => {
          if (!gone) setMap((s) => ({ ...s, [p]: r.models }))
        })
        .catch(() => {})
    }
    return () => {
      gone = true
    }
  }, [key])
  return map
}
