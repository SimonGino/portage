/** 模型目录胶囊取值（#189，DESIGN v0.76）：实底/虚线灰字/不摆、图片模态、单价含分档。 */

import { describe, expect, it } from 'vitest'
import { catalogPills } from './catalog'
import type { CatalogModel } from './api'

const row = (over: Partial<CatalogModel>): CatalogModel => ({
  id: 'gw-x',
  direct: false,
  protocols: ['anthropic'],
  max_input_tokens: 0,
  price_input: null,
  price_output: null,
  price_cache_read: null,
  price_cache_write: null,
  price_tier_above: null,
  price_tier_input: null,
  price_tier_output: null,
  price_tier_cache_read: null,
  price_tier_cache_write: null,
  snapshot_context: 0,
  image: false,
  ...over,
})

describe('输入上限胶囊三态', () => {
  it('条目已设 → 实底', () => {
    const p = catalogPills(row({ max_input_tokens: 300000 }))
    expect(p.context?.ghost).toBe(false)
    expect(p.context?.text).toBe('输入 ≤300k')
  })
  it('未设而快照有 limit.context → 虚线灰字「≈N · models.dev」', () => {
    const p = catalogPills(row({ snapshot_context: 1000000 }))
    expect(p.context?.ghost).toBe(true)
    expect(p.context?.text).toBe('≈1M · models.dev')
  })
  it('两样都没有 → 不摆，不猜', () => {
    expect(catalogPills(row({})).context).toBeNull()
  })
  it('条目已设时快照值不上桌（快照只是提示，不覆盖人的设置）', () => {
    const p = catalogPills(row({ max_input_tokens: 300000, snapshot_context: 1000000 }))
    expect(p.context?.text).toBe('输入 ≤300k')
  })
})

describe('协议子集与图片模态', () => {
  it('协议短名映射：openai_responses → Responses；不认识的照原样', () => {
    const p = catalogPills(row({ protocols: ['openai_responses', 'custom-x'] }))
    expect(p.protocols.map((x) => x.label)).toEqual(['Responses', 'custom-x'])
  })
  it('图片小图标只在快照 modalities.input 含 image 时摆', () => {
    expect(catalogPills(row({ image: true })).image).toBe(true)
    expect(catalogPills(row({})).image).toBe(false)
  })
})

describe('单价胶囊（NULL ≠ 0，口径层两态）', () => {
  it('$入/$出，分档加「· >200k ↑」后缀', () => {
    const p = catalogPills(
      row({ price_input: 3, price_output: 15, price_tier_above: 200000 }),
    )
    expect(p.unpriced).toBe(false)
    expect(p.price?.text).toBe('$3/$15 · >200k ↑')
    expect(p.price?.title).toContain('缓读 —')
  })
  it('四价全 null → 「未定价」，不显 $0', () => {
    const p = catalogPills(row({}))
    expect(p.unpriced).toBe(true)
    expect(p.price).toBeNull()
  })
  it('0 是真免费，照摆 $0', () => {
    const p = catalogPills(row({ price_input: 0, price_output: 0 }))
    expect(p.unpriced).toBe(false)
    expect(p.price?.text).toBe('$0/$0')
  })
})
