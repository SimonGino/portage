// 排行页的区间数学。**这个文件不 import React**：铺格子、分档、算统计量这些
// 是「数字错了且看不出来」的一类，抽成纯函数才测得动（intervals.test.ts）。
//
// 枢纽是「区间（interval）」这一个抽象（口径层 v0.86 ②）：一个桶数组喂三种排布，
// 一套统计文案吃三档窗口。三种排布只是把同一个 Interval[] 摆成柱 / 点阵 / 日历。

import type { BucketUsage, UsageRow } from '../../api'

/** 分桶粒度，与后端 store.BucketDay / BucketHour 同名同值。 */
export type Unit = 'hour' | 'day'

/**
 * 节律带上的一格。
 *
 * `future` 是**第三档**，不是「0 次调用」的同义词：还没走到的那几个钟点不画、
 * 不可点、不进活跃区间的分母（口径层 v0.86 ③，v0.55 的自然日窗口直接管这里）。
 * 参照面板那张恒取满格，于是它的「活跃 11/24」在一天过完之前永远被低估。
 */
export interface Interval {
  /** 本地时区的区间起点。 */
  start: Date
  /** 半开区间的右端，就是下一格的起点。 */
  end: Date
  future: boolean
  calls: number
  errors: number
  /** 毛值，含缓存读写两项（口径层 v0.71）。 */
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  /** input + output，与排行列表的主指标同一个口径。 */
  tokens: number
}

/**
 * 窗口档位 → 区间粒度与格数。1 天 = 24 个 1 小时，7 天 = 168 个 1 小时，
 * 30 天 = 30 个 1 天（口径层 v0.86 ②）。
 */
export function windowSpec(days: number): { unit: Unit; count: number } {
  return days > 7 ? { unit: 'day', count: days } : { unit: 'hour', count: days * 24 }
}

/**
 * 窗口左端：本地自然日的 00:00 往回数 days-1 天（口径层 v0.55「今天算一天」）。
 *
 * 不能拿「最后 168 个小时桶」顶替：那样第 0 列是「七天前的 16 点」而不是 0 点，
 * 点阵横轴那排 00/03/06 会整体错位——图还在，但它说的每一句都是假的。
 */
export function windowStart(days: number, now: Date): Date {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  d.setDate(d.getDate() - (days - 1))
  return d
}

const p2 = (n: number) => String(n).padStart(2, '0')

/**
 * 桶键：与后端 `store.BucketUsage.Bucket` **逐字符同款**（`YYYY-MM-DD` /
 * `YYYY-MM-DD HH:00`，本地时区）。
 *
 * 前端自己按本地时间铺格子再拿这个键去查，**从不 `new Date(row.bucket)`**：
 * `"2026-08-12 00:00"` 不是 ISO 串，各家浏览器解析它的时区口径不一致，Safari 上
 * 直接 Invalid Date。
 */
export function bucketKey(d: Date, unit: Unit): string {
  const day = `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`
  return unit === 'hour' ? `${day} ${p2(d.getHours())}:00` : day
}

/** 从窗口左端往后数 i 格。用本地日历加法，不做毫秒乘法。 */
function slotStart(start: Date, unit: Unit, i: number): Date {
  const d = new Date(start)
  if (unit === 'hour') d.setHours(start.getHours() + i)
  else d.setDate(start.getDate() + i)
  return d
}

/**
 * 把后端那串桶铺成固定长度的格子。
 *
 * 后端只回**已经发生**的桶（#21：空桶补零、未到的不给），格子怎么排是前端的事：
 * 铺不满的那几格是**空位**（future）不是零值。
 */
export function buildIntervals(
  days: number,
  rows: BucketUsage[],
  now: Date,
): { unit: Unit; list: Interval[] } {
  const { unit, count } = windowSpec(days)
  const got = new Map(rows.map((r) => [r.bucket, r]))
  const start = windowStart(days, now)
  const list: Interval[] = []
  for (let i = 0; i < count; i++) {
    const s = slotStart(start, unit, i)
    const r = got.get(bucketKey(s, unit))
    list.push({
      start: s,
      end: slotStart(start, unit, i + 1),
      // 起点晚于此刻就是还没到。天粒度下今天那一格的起点是今天 00:00，属于已过去。
      future: s.getTime() > now.getTime(),
      calls: r?.calls ?? 0,
      errors: r?.errors ?? 0,
      input: r?.input_tokens ?? 0,
      output: r?.output_tokens ?? 0,
      cacheRead: r?.cache_read_tokens ?? 0,
      cacheWrite: r?.cache_write_tokens ?? 0,
      tokens: (r?.input_tokens ?? 0) + (r?.output_tokens ?? 0),
    })
  }
  return { unit, list }
}

/** 选中某一格之后拿去重取维度汇总的半开区间（unix 秒，对上 `/usage?from=&to=`）。 */
export function intervalRange(b: Interval): { from: number; to: number } {
  return { from: Math.floor(b.start.getTime() / 1000), to: Math.floor(b.end.getTime() / 1000) }
}

/** 五个统计量（口径层 v0.86 ④），全部出自同一个数组，后端不多给一个数。 */
export interface IvStats {
  total: number
  /** 每区间均值，分母同 n。 */
  avg: number
  /**
   * 峰值区间。**没有哪一格报出过 token 时是 null**，界面上写「—」而不是
   * 「0 · 8月18日 00时」——那个钟点没有任何理由被指认成峰值，与每行占比在整段
   * 不报 usage 时写「—」是同一条。
   */
  peak: Interval | null
  activeN: number
  /** 活跃区间的分母：**只数已经过去的区间**。 */
  n: number
  /** 最长连续：连着几个区间有调用。 */
  streak: number
}

export function ivStats(list: Interval[]): IvStats {
  const past = list.filter((b) => !b.future)
  const total = past.reduce((a, b) => a + b.tokens, 0)
  let peak: Interval | null = null
  let streak = 0
  let best = 0
  for (const b of past) {
    if (b.tokens > 0 && (!peak || b.tokens > peak.tokens)) peak = b
    streak = b.calls > 0 ? streak + 1 : 0
    if (streak > best) best = streak
  }
  return {
    total,
    avg: past.length ? Math.round(total / past.length) : 0,
    peak,
    activeN: past.filter((b) => b.calls > 0).length,
    n: past.length,
    streak: best,
  }
}

/**
 * 热度分档。返回值有六档，别把前三档混成一档：
 *
 * - `-2` 还没到（不画、不可点、不进分母）
 * - `-1` 没有调用（空格）
 * - `0` 有调用但上游没报 token（最浅一档，仍占位）
 * - `1…4` 有 token，**按分位切**
 *
 * 分位不是把最大值等分：一段冲刺会把 max 拉到天上，等分之后其余全落最低档，
 * 整张图变成「一个亮点 + 一片灰」。四个切点取 0.25 / 0.5 / 0.75 / 0.92。
 */
export function heatScale(list: Interval[]): (b: Interval) => number {
  const vals = list
    .filter((b) => !b.future && b.tokens > 0)
    .map((b) => b.tokens)
    .sort((a, b) => a - b)
  const q = (p: number) => vals[Math.min(vals.length - 1, Math.floor(vals.length * p))]
  const cuts = vals.length ? [q(0.25), q(0.5), q(0.75), q(0.92)] : []
  return (b) => {
    if (b.future) return -2
    if (b.calls === 0) return -1
    if (b.tokens === 0) return 0
    return b.tokens <= cuts[0] ? 1 : b.tokens <= cuts[1] ? 2 : b.tokens <= cuts[2] ? 3 : 4
  }
}

/** 切片井那条构成条的三段。 */
export interface SliceComp {
  cacheRead: number
  cacheWrite: number
  cacheAll: number
  /** 净输入 = 毛值 − 缓存两项（口径层 v0.71）。 */
  netIn: number
  out: number
  total: number
}

/**
 * 「这一段是怎么构成的」：缓存 / 净输入 / 输出三段。
 *
 * 不列 reasoning（口径层 v0.66：思考在 output 里面，单列会被读成第四笔加数）。
 * 净输入夹到 0 以上——真有上游把 input 报成不含缓存的净值时，减出来是负的，
 * 那一段会画成负宽度。
 */
export function sliceComposition(b: Interval): SliceComp {
  const cacheAll = b.cacheRead + b.cacheWrite
  return {
    cacheRead: b.cacheRead,
    cacheWrite: b.cacheWrite,
    cacheAll,
    netIn: Math.max(0, b.input - cacheAll),
    out: b.output,
    total: b.tokens,
  }
}

// ── 分类色盘 ──────────────────────────────────────────────────────────
//
// DESIGN §4 给排行页开的唯一口子（口径层 v0.86 ⑨）：这五个色承载的是**身份**
// 不是语义，用途封死在环形图与两条堆叠条里，不外溢到列表、状态、热度任何一处。
// 色值以在羊皮纸画布 #f5f4ed 上跑过 dataviz 校验器的那五个为准，见 styles.css。

export const CAT_COLORS = [
  'var(--cat-1)',
  'var(--cat-2)',
  'var(--cat-3)',
  'var(--cat-4)',
  'var(--cat-5)',
]
export const OTHER_COLOR = 'var(--cat-other)'

/** 一行的 token 主指标：与排行列表的名次口径同一个。 */
export const tokensOf = (r: UsageRow) => r.input_tokens + r.output_tokens

/**
 * 名次按 **token 总量** 排，并列时退回调用次数——上游整段不报 usage 时所有行的
 * token 都是 0，那时次数是唯一还有区分度的数。
 */
export function rankSort(rows: UsageRow[]): UsageRow[] {
  return [...rows].sort((a, b) => tokensOf(b) - tokensOf(a) || b.calls - a.calls)
}

/**
 * 色映射。**拿整窗聚合算一次，之后只查表**：选中某个区间会让排行重新排序，
 * 按名次上色的话整排条会同时改色，「蓝色那条」在两次点击之间指的就不是同一个
 * 东西了（口径层 v0.86，DESIGN §4）。
 *
 * 第六名起一律并进「其余」的中性灰，不生成第六个色相。
 */
export function colorMap(windowRows: UsageRow[]): Map<string, string> {
  return new Map(
    rankSort(windowRows).map((r, i) => [r.label, i < CAT_COLORS.length ? CAT_COLORS[i] : OTHER_COLOR]),
  )
}

/** 环与堆叠条上的一段。 */
export interface CatSlice {
  label: string
  tokens: number
  color: string
}

/**
 * 一段时间的构成：前五名各一段，其余合并成一段。
 *
 * 归不归「其余」看的是**色映射**而不是这一段自己的名次——名次跟着 scope 变，
 * 色不变，两者混用会让某个模型在切片里突然分到一个色相。
 */
export function composition(
  rows: UsageRow[],
  color: Map<string, string>,
): { slices: CatSlice[]; total: number } {
  const head: CatSlice[] = []
  let otherTokens = 0
  let otherCount = 0
  for (const r of rankSort(rows)) {
    const t = tokensOf(r)
    if (t <= 0) continue
    const c = color.get(r.label) ?? OTHER_COLOR
    if (c === OTHER_COLOR) {
      otherTokens += t
      otherCount++
      continue
    }
    head.push({ label: r.label, tokens: t, color: c })
  }
  if (otherCount > 0) {
    head.push({ label: `其余（${otherCount}）`, tokens: otherTokens, color: OTHER_COLOR })
  }
  return { slices: head, total: head.reduce((a, s) => a + s.tokens, 0) }
}

// ── 日历排布 ──────────────────────────────────────────────────────────

/** 日历上的一格。`index` 是它在 Interval[] 里的下标，-1 表示不在这个窗口里。 */
export interface CalCell {
  date: Date
  index: number
}

/**
 * 30 天那档的排布：**只画窗口盖住的那六周**，不做 53 周画布（口径层 v0.86 ⑤）。
 * 本项目这个数据量下一年画布 94% 的格子是灰的，一屏里最大的一块图基本没信息。
 *
 * 画布右端钉在本周周六，这样最后一列是完整的一周；窗口外那几格仍留位——一周
 * 七格的结构不能缺角。月份标签打在该月**第一次出现**的那一列。
 */
export function calendarWeeks(
  list: Interval[],
  now: Date,
  weeks = 6,
): { weeks: CalCell[][]; months: { col: number; label: string }[] } {
  const dayKey = (d: Date) => bucketKey(d, 'day')
  const at = new Map(list.map((b, i) => [dayKey(b.start), i]))

  const end = new Date(now)
  end.setHours(0, 0, 0, 0)
  end.setDate(end.getDate() + (6 - end.getDay()))

  const grid: CalCell[][] = []
  const months: { col: number; label: string }[] = []
  let lastMonth = -1
  for (let w = weeks - 1; w >= 0; w--) {
    const col = weeks - w
    const days: CalCell[] = []
    for (let d = 0; d < 7; d++) {
      const day = new Date(end)
      day.setDate(end.getDate() - w * 7 + d - 6)
      days.push({ date: day, index: at.get(dayKey(day)) ?? -1 })
    }
    const month = days[6].date.getMonth()
    if (month !== lastMonth) months.push({ col, label: `${month + 1}月` })
    lastMonth = month
    grid.push(days)
  }
  return { weeks: grid, months }
}
