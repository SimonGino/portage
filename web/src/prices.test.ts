import { describe, expect, it } from 'vitest'
import { fmtTierTitle, fmtTokensShort, parsePriceDraft, providerName, providerOptions, samePrices, suggestPriceBody } from './prices'

const list = [
  { id: '302ai', name: '302 AI' },
  { id: 'openai', name: 'OpenAI' },
]

describe('providerOptions', () => {
  it('「未标注」打头，名单按序跟上，hint 是 id', () => {
    expect(providerOptions(list, '')).toEqual([
      { value: '', label: '未标注' },
      { value: '302ai', label: '302 AI', hint: '302ai' },
      { value: 'openai', label: 'OpenAI', hint: 'openai' },
    ])
  })

  it('当前值在名单里就不补项', () => {
    expect(providerOptions(list, 'openai')).toHaveLength(3)
  })

  it('当前值不在名单里：补一项回去并注明「快照名单外」，触发器不会显示成「未标注」', () => {
    expect(providerOptions(list, 'mystery')[1]).toEqual({ value: 'mystery', label: 'mystery', hint: '快照名单外' })
    // 名单还没拉到（空数组）同样补，库里明明有值不能显示成未标注。
    expect(providerOptions([], 'openai')).toEqual([
      { value: '', label: '未标注' },
      { value: 'openai', label: 'openai', hint: '快照名单外' },
    ])
  })
})

describe('providerName', () => {
  it('名单里有摆人话名，没有（或没拉到）摆 id 本身', () => {
    expect(providerName(list, '302ai')).toBe('302 AI')
    expect(providerName(list, 'mystery')).toBe('mystery')
    expect(providerName([], 'openai')).toBe('openai')
  })
})

describe('分档价（#185）', () => {
  const base = { input: '3', output: '15', cache_read: '', cache_write: '0' }

  it('阈值简写：整千写 k，整百万写 M', () => {
    expect(fmtTokensShort(200000)).toBe('200k')
    expect(fmtTokensShort(1000000)).toBe('1M')
    expect(fmtTokensShort(1500)).toBe('1500')
  })

  it('分档全文：缺价写沿用基础价，给 #195 的目录胶囊复用', () => {
    const b = suggestPriceBody({ input: 3, tier: { above: 200000, input: 6, output: 22.5 } })
    expect(fmtTierTitle(b)).toBe('超过 200000 token 整笔按：入 $6，出 $22.5，缓读 沿用基础价，缓写 沿用基础价')
  })

  it('清空阈值 = 去分档：分档四价与分档 1h 缓写跟着落 null', () => {
    expect(parsePriceDraft({ ...base, cache_write_1h: '6', tier_above: '', tier_input: '6' })).toEqual({
      input: 3, output: 15, cache_read: null, cache_write: 0, cache_write_1h: 6,
      tier_above: null, tier_input: null, tier_output: null, tier_cache_read: null, tier_cache_write: null,
      tier_cache_write_1h: null,
    })
  })

  it('有阈值时分档某价空 = null（沿用基础价），0 照写', () => {
    expect(parsePriceDraft({ ...base, tier_above: '200000', tier_input: '6', tier_cache_read: '0' })).toMatchObject({
      tier_above: 200000, tier_input: 6, tier_output: null, tier_cache_read: 0, tier_cache_write: null,
      cache_write_1h: null, tier_cache_write_1h: null,
    })
  })

  it('阈值非正整数、价解析不出整组不存', () => {
    expect(parsePriceDraft({ ...base, tier_above: '0' })).toBeNull()
    expect(parsePriceDraft({ ...base, tier_above: '1.5' })).toBeNull()
    expect(parsePriceDraft({ ...base, tier_above: '1000', tier_output: '1..2' })).toBeNull()
  })

  it('建议价带分档：阈值与分档四价一起进采纳的那组，缺价 null', () => {
    const b = suggestPriceBody({ input: 2, tier: { above: 128000, output: 8 } })
    expect(b).toMatchObject({ input: 2, output: null, tier_above: 128000, tier_input: null, tier_output: 8 })
    expect(samePrices(b, { ...b })).toBe(true)
    expect(samePrices(b, { ...b, tier_above: null })).toBe(false)
  })
})

describe('1h 缓写（#198）', () => {
  const base = { input: '3', output: '15', cache_read: '', cache_write: '3.75', cache_write_1h: '6' }

  it('基础 1h 缓写进整组；分档带 1h 时有值才进 title，缺价不摆字', () => {
    const b = parsePriceDraft({ ...base, tier_above: '200000', tier_input: '6', tier_cache_write_1h: '12' })
    expect(b).not.toBeNull()
    expect(b).toMatchObject({ cache_write_1h: 6, tier_cache_write_1h: 12 })
    expect(fmtTierTitle(b!)).toBe(
      '超过 200000 token 整笔按：入 $6，出 沿用基础价，缓读 沿用基础价，缓写 沿用基础价，1h 缓写 $12',
    )
    expect(fmtTierTitle(parsePriceDraft({ ...base, tier_above: '200000' })!)).toBe(
      '超过 200000 token 整笔按：入 沿用基础价，出 沿用基础价，缓读 沿用基础价，缓写 沿用基础价',
    )
  })

  it('建议价连 1h 一起进采纳：claude 系后端派生 2× input，非 claude 缺键 = null', () => {
    const claude = suggestPriceBody({ input: 3, cache_write_1h: 6, tier: { above: 200000, input: 6, cache_write_1h: 12 } })
    expect(claude).toMatchObject({ cache_write_1h: 6, tier_cache_write_1h: 12 })
    const other = suggestPriceBody({ input: 2 })
    expect(other.cache_write_1h).toBeNull()
    expect(other.tier_cache_write_1h).toBeNull()
    // 十一键全参与相等判定：仅差 1h 也算不等，「采纳」才会摆出来。
    expect(samePrices(claude, { ...claude, cache_write_1h: null })).toBe(false)
  })

  it('留空 1h = 未设（按 5 分钟价计），0 = 真免费', () => {
    expect(parsePriceDraft({ ...base, cache_write_1h: '' })).toMatchObject({ cache_write_1h: null })
    expect(parsePriceDraft({ ...base, cache_write_1h: '0' })).toMatchObject({ cache_write_1h: 0 })
    expect(parsePriceDraft({ ...base, cache_write_1h: '-1' })).toBeNull()
  })
})
