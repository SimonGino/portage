import { describe, expect, it } from 'vitest'
import type { BucketUsage, UsageRow } from '../../api'
import {
  buildIntervals,
  calendarWeeks,
  colorMap,
  composition,
  heatScale,
  intervalRange,
  ivStats,
  sliceComposition,
  OTHER_COLOR,
} from './intervals'

// 固定「现在」：2026-08-18（周二）15:30 本地。口径层 v0.86 ③ 那条验收就钉在这个钟点上。
const NOW = new Date(2026, 7, 18, 15, 30, 0)

function bucket(b: string, over: Partial<BucketUsage> = {}): BucketUsage {
  return {
    bucket: b,
    calls: 1,
    errors: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    ...over,
  }
}

function usage(label: string, tokens: number, over: Partial<UsageRow> = {}): UsageRow {
  return {
    label,
    calls: 1,
    errors: 0,
    input_tokens: tokens,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cost_usd: 0,
    ...over,
  }
}

describe('buildIntervals', () => {
  it('1 天铺 24 格，15:30 时只有 16 格是已经过去的', () => {
    const { unit, list } = buildIntervals(1, [], NOW)
    expect(unit).toBe('hour')
    expect(list).toHaveLength(24)
    expect(list.filter((b) => !b.future)).toHaveLength(16)
    // 16 点那一格是「还没到」，不是「0 次调用」。
    expect(list[15].future).toBe(false)
    expect(list[16].future).toBe(true)
    expect(ivStats(list).n).toBe(16)
  })

  it('桶键按后端那两种格式拼，不靠 new Date(bucket) 解析', () => {
    const rows = [bucket('2026-08-18 09:00', { calls: 7, input_tokens: 100, output_tokens: 20 })]
    const { list } = buildIntervals(1, rows, NOW)
    expect(list[9].calls).toBe(7)
    expect(list[9].tokens).toBe(120)
    expect(list[8].calls).toBe(0)
    // 前导零：8 点必须是 "08:00" 而不是 "8:00"，否则整列对不上。
    const { list: hit } = buildIntervals(1, [bucket('2026-08-18 08:00', { calls: 3 })], NOW)
    expect(hit[8].calls).toBe(3)
  })

  it('7 天从 6 天前的 00:00 起铺 168 格', () => {
    const { unit, list } = buildIntervals(7, [bucket('2026-08-12 00:00', { calls: 5 })], NOW)
    expect(unit).toBe('hour')
    expect(list).toHaveLength(168)
    expect(list[0].start.getTime()).toBe(new Date(2026, 7, 12, 0, 0, 0).getTime())
    expect(list[0].calls).toBe(5)
    expect(list.filter((b) => !b.future)).toHaveLength(6 * 24 + 16)
  })

  it('30 天铺 30 个天桶，今天那一格算已经过去的', () => {
    const { unit, list } = buildIntervals(30, [bucket('2026-08-18', { calls: 9 })], NOW)
    expect(unit).toBe('day')
    expect(list).toHaveLength(30)
    expect(list[29].calls).toBe(9)
    expect(list[29].future).toBe(false)
    expect(list.every((b) => !b.future)).toBe(true)
  })

  it('区间是半开的，右端就是下一格的起点', () => {
    const { list } = buildIntervals(1, [], NOW)
    const r = intervalRange(list[9])
    expect(r.from).toBe(Math.floor(new Date(2026, 7, 18, 9).getTime() / 1000))
    expect(r.to).toBe(Math.floor(new Date(2026, 7, 18, 10).getTime() / 1000))
    expect(r.to).toBe(Math.floor(list[10].start.getTime() / 1000))
  })
})

describe('ivStats', () => {
  it('分母只数已经过去的区间，均值也跟着这个分母', () => {
    const rows = [
      bucket('2026-08-18 00:00', { calls: 2, input_tokens: 100 }),
      bucket('2026-08-18 01:00', { calls: 2, input_tokens: 300 }),
    ]
    const s = ivStats(buildIntervals(1, rows, NOW).list)
    expect(s.n).toBe(16)
    expect(s.activeN).toBe(2)
    expect(s.total).toBe(400)
    expect(s.avg).toBe(25) // 400 / 16，不是 400 / 24 也不是 400 / 2
    expect(s.peak?.tokens).toBe(300)
  })

  it('最长连续只数连片的活跃区间', () => {
    const rows = [0, 1, 2, 5, 6].map((h) =>
      bucket(`2026-08-18 ${String(h).padStart(2, '0')}:00`, { calls: 1 }),
    )
    expect(ivStats(buildIntervals(1, rows, NOW).list).streak).toBe(3)
  })

  it('一次调用都没有时不炸，峰值为空', () => {
    const s = ivStats(buildIntervals(1, [], NOW).list)
    expect(s).toMatchObject({ total: 0, activeN: 0, n: 16, streak: 0, avg: 0 })
    expect(s.peak).toBeNull()
  })
})

describe('heatScale', () => {
  it('三档语义分开：还没到 / 没有调用 / 有调用但没报 token', () => {
    const rows = [
      bucket('2026-08-18 00:00', { calls: 4, input_tokens: 0, output_tokens: 0 }),
      bucket('2026-08-18 01:00', { calls: 4, input_tokens: 500 }),
    ]
    const { list } = buildIntervals(1, rows, NOW)
    const lv = heatScale(list)
    expect(lv(list[16])).toBe(-2) // 还没到
    expect(lv(list[2])).toBe(-1) // 没有调用
    expect(lv(list[0])).toBe(0) // 有调用，上游没报 token
    expect(lv(list[1])).toBeGreaterThan(0)
  })

  it('按分位切档：一个尖峰不该把其余全压进最浅一档', () => {
    const rows = Array.from({ length: 12 }, (_, i) =>
      bucket(`2026-08-18 ${String(i).padStart(2, '0')}:00`, {
        calls: 1,
        input_tokens: i === 11 ? 1_000_000 : (i + 1) * 10,
      }),
    )
    const { list } = buildIntervals(1, rows, NOW)
    const lv = heatScale(list)
    const levels = list.slice(0, 12).map(lv)
    // 等分之下这 11 个小值全是第 1 档；分位切档必须把它们摊到四档上。
    expect(new Set(levels).size).toBeGreaterThanOrEqual(4)
    expect(lv(list[11])).toBe(4)
    expect(lv(list[0])).toBe(1)
  })

  it('一格 token 都没有时仍分得清三档', () => {
    const { list } = buildIntervals(1, [bucket('2026-08-18 00:00', { calls: 2 })], NOW)
    const lv = heatScale(list)
    expect(lv(list[0])).toBe(0)
    expect(lv(list[1])).toBe(-1)
    expect(lv(list[16])).toBe(-2)
  })
})

describe('colorMap / composition', () => {
  const win = [
    usage('claude-opus-5', 440),
    usage('gpt-5.6-sol', 270),
    usage('claude-fable-5', 140),
    usage('glm-4.6', 80),
    usage('kimi-k2', 50),
    usage('haiku', 20),
    usage('minimax', 10),
  ]

  it('色跟着实体走：换一个 scope 重排名次，颜色不动', () => {
    const map = colorMap(win)
    const opus = map.get('claude-opus-5')
    // 切片里 gpt 排第一了，但查的仍是同一张表。
    const scope = [usage('gpt-5.6-sol', 900), usage('claude-opus-5', 100)]
    const c = composition(scope, map)
    expect(c.slices.find((s) => s.label === 'claude-opus-5')?.color).toBe(opus)
    expect(c.slices[0].label).toBe('gpt-5.6-sol')
    expect(c.slices[0].color).toBe(map.get('gpt-5.6-sol'))
  })

  it('第六名起并进「其余（N）」，不发第六个色相', () => {
    const c = composition(win, colorMap(win))
    expect(c.slices).toHaveLength(6)
    expect(c.slices[5]).toMatchObject({ label: '其余（2）', color: OTHER_COLOR, tokens: 30 })
    expect(new Set(c.slices.slice(0, 5).map((s) => s.color)).size).toBe(5)
    expect(c.total).toBe(1010)
  })

  it('空的一段既不给段也不给合计', () => {
    const c = composition([], colorMap(win))
    expect(c.slices).toHaveLength(0)
    expect(c.total).toBe(0)
  })
})

describe('sliceComposition', () => {
  it('净输入 = 毛值 − 缓存两项，三段加起来正好是合计', () => {
    const { list } = buildIntervals(
      1,
      [
        bucket('2026-08-18 00:00', {
          input_tokens: 1000,
          output_tokens: 200,
          cache_read_tokens: 600,
          cache_write_tokens: 100,
        }),
      ],
      NOW,
    )
    const c = sliceComposition(list[0])
    expect(c).toMatchObject({ cacheRead: 600, cacheWrite: 100, cacheAll: 700, netIn: 300, out: 200 })
    expect(c.cacheAll + c.netIn + c.out).toBe(c.total)
  })

  it('上游报的缓存超过毛值时净输入夹到 0，不画成负宽', () => {
    const { list } = buildIntervals(
      1,
      [bucket('2026-08-18 00:00', { input_tokens: 100, cache_read_tokens: 900 })],
      NOW,
    )
    expect(sliceComposition(list[0]).netIn).toBe(0)
  })
})

describe('calendarWeeks', () => {
  it('只画窗口盖住的那六周，窗口外的格子留位不给下标', () => {
    const { list } = buildIntervals(30, [], NOW)
    const cal = calendarWeeks(list, NOW)
    expect(cal.weeks).toHaveLength(6)
    expect(cal.weeks.every((w) => w.length === 7)).toBe(true)
    const inWindow = cal.weeks.flat().filter((c) => c.index >= 0)
    expect(inWindow).toHaveLength(30)
    // 最后一列的最后一格是本周六，2026-08-22。
    const last = cal.weeks[5][6]
    expect(last.date.getTime()).toBe(new Date(2026, 7, 22).getTime())
    expect(last.index).toBe(-1) // 还没到，不在窗口里
    // 每一格的下标必须真的指回那一天。
    for (const c of inWindow) {
      expect(list[c.index].start.getTime()).toBe(c.date.getTime())
    }
  })

  it('月份标签打在该月第一次出现的那一列', () => {
    const cal = calendarWeeks(buildIntervals(30, [], NOW).list, NOW)
    expect(cal.months.map((m) => m.label)).toEqual(['7月', '8月'])
    expect(cal.months[0].col).toBe(1)
  })
})
