// 节律带的三种排布、切片井、环形图与堆叠条。都是纯展示：数怎么来的在
// intervals.ts，接口怎么调的在 Rankings.tsx，这里只管把一个 Interval[] 摆成图。
//
// 不引图表库，手写 SVG / CSS——与原先概览页那张 usage-chart 同一路子。

import type { CalCell, Composition, Interval, SliceComp, Unit } from './intervals'
import { fmtCompact, fmtInt } from '../../ui'

const p2 = (n: number) => String(n).padStart(2, '0')
const WD = ['日', '一', '二', '三', '四', '五', '六']

/** 「8/18」。节律带上到处是日期，写全年月日会把 34px 的格子撑破。 */
export const md = (d: Date) => `${d.getMonth() + 1}/${d.getDate()}`

/** 一格的提示语：什么时候 · 多少次 · 多少 token。三种排布共用一句。 */
function tip(b: Interval, unit: Unit) {
  const when = unit === 'hour' ? `${md(b.start)} ${p2(b.start.getHours())}:00` : md(b.start)
  if (b.future) return `${when} · 还没到`
  const bad = b.errors ? ` · 失败 ${fmtInt(b.errors)}` : ''
  return `${when} · ${fmtInt(b.calls)} 次 · ${fmtInt(b.tokens)} token${bad}`
}

/** 峰值 / 切片井的时间文案。天粒度不报钟点。 */
export function whenLabel(b: Interval, unit: Unit, long = false) {
  const day = long
    ? `${b.start.getMonth() + 1} 月 ${b.start.getDate()} 日 周${WD[b.start.getDay()]}`
    : md(b.start)
  if (unit === 'day') return day
  const h = p2(b.start.getHours())
  return long ? `${day} · ${h}:00 – ${h}:59` : `${day} ${h} 时`
}

interface SkyProps {
  list: Interval[]
  /** 热度分档，取值见 intervals.heatScale。 */
  scale: (b: Interval) => number
  sel: number | null
  onPick: (i: number) => void
}

/**
 * 排布①：1 天 = 24 根柱，横轴小时。
 *
 * 柱靠**高度**说话，不叠热度色——同一个量编码两遍不多给一分信息。
 * 「有调用但上游没报 token」留一线高度，让它仍占一格位置；「没有调用」是真的 0。
 */
export function SkyBars({ list, sel, onPick }: Omit<SkyProps, 'scale'>) {
  const max = Math.max(...list.map((b) => b.tokens), 1)
  return (
    <>
      <div className="sky-bars">
        {list.map((b, i) =>
          b.future ? (
            <span className="sky-bar-col" key={i} title={tip(b, 'hour')} />
          ) : (
            <button
              type="button"
              key={i}
              className={'sky-bar-col' + (sel === i ? ' is-sel' : '')}
              data-iv={i}
              aria-pressed={sel === i}
              title={tip(b, 'hour')}
              onClick={() => onPick(i)}
            >
              {/* 三档在这一排布里由**高度**分开，不由深浅（深浅是点阵与日历的活）：
                  有 token 按比例、至少 2%；有调用但上游没报 token 留 2% 一线，
                  仍占一格位置；没有调用是真的 0，画不出东西。 */}
              <span
                className="sky-bar"
                style={{ height: `${b.tokens ? Math.max((b.tokens / max) * 100, 2) : b.calls ? 2 : 0}%` }}
              />
            </button>
          ),
        )}
      </div>
      <div className="sky-ticks" style={{ gridTemplateColumns: `repeat(${list.length},1fr)` }}>
        {list.map((b, i) => (
          <span key={i}>{i % 3 === 0 ? `${p2(b.start.getHours())}:00` : ''}</span>
        ))}
      </div>
    </>
  )
}

/**
 * 排布②：7 天 = 7 行 × 24 列点阵，**尺寸 + 深浅双编码**。
 *
 * 两条腿是刻意的：小屏与低对比屏上深浅先糊，尺寸还在；打印出来反过来。
 * 只留一条的话最小那档字号下就读不出昼夜节律了。
 */
export function SkyMatrix({ list, scale, sel, onPick }: SkyProps) {
  const rows = Array.from({ length: 7 }, (_, d) => list.slice(d * 24, d * 24 + 24))
  return (
    <div className="sky-grid">
      {rows.map((row, d) => (
        <div className="sky-rowlab-row" key={d}>
          <div className="sky-rowlab">
            周{WD[row[0].start.getDay()]} {row[0].start.getDate()}
          </div>
          <div className="sky-row">
            {row.map((b, c) => {
              const i = d * 24 + c
              if (b.future) return <span className="sky-dot-hit" key={c} title={tip(b, 'hour')} />
              const lv = scale(b)
              return (
                <button
                  type="button"
                  key={c}
                  className={'sky-dot-hit' + (sel === i ? ' is-sel' : '')}
                  data-iv={i}
                  aria-pressed={sel === i}
                  title={tip(b, 'hour')}
                  onClick={() => onPick(i)}
                >
                  <span
                    className="sky-dot"
                    style={{
                      width: lv < 0 ? 4 : 5 + lv * 2.6,
                      height: lv < 0 ? 4 : 5 + lv * 2.6,
                      background: lv < 0 ? 'var(--heat-none-dot)' : `var(--heat-${lv})`,
                    }}
                  />
                </button>
              )
            })}
          </div>
        </div>
      ))}
      <div className="sky-rowlab-row">
        <div className="sky-rowlab" />
        <div className="sky-row sky-ticks">
          {Array.from({ length: 24 }, (_, h) => (
            <span key={h}>{h % 3 === 0 ? p2(h) : ''}</span>
          ))}
        </div>
      </div>
    </div>
  )
}

/**
 * 排布③：30 天 = 窗口盖住的那六周日历（口径层 v0.86 ⑤，不做 53 周画布），
 * 按 GitHub 贡献图式裁切画（DESIGN v0.31）。
 *
 * 小格、窄隙、左对齐；窗口外的格裁成透明——位仍占着（一周七格不能缺角），但
 * 不描边、不填底、无悬停：留任何痕迹裁切就假了。星期只标一/三/五。
 */
export function SkyCalendar({
  list,
  scale,
  sel,
  onPick,
  weeks,
  months,
}: SkyProps & { weeks: CalCell[][]; months: { col: number; label: string }[] }) {
  return (
    <div className="cal">
      <div />
      <div
        className="cal-months"
        style={{ gridTemplateColumns: `repeat(${weeks.length},calc(var(--cell) + var(--cal-gap)))` }}
      >
        {months.map((m) => (
          <span key={m.col} style={{ gridColumn: m.col }}>
            {m.label}
          </span>
        ))}
      </div>
      {/* 这个密度下只标一/三/五，与 GitHub 贡献图同一节奏。 */}
      <div className="cal-wdays">
        {['', '一', '', '三', '', '五', ''].map((w, i) => (
          <span key={i}>{w}</span>
        ))}
      </div>
      <div className="cal-body">
        {weeks.map((week, w) => (
          <div className="cal-week" key={w}>
            {week.map((c) => {
              const b = c.index >= 0 ? list[c.index] : null
              if (!b) {
                return <span className="cal-day is-gap" key={c.date.getTime()} />
              }
              const lv = scale(b)
              return (
                <button
                  type="button"
                  key={c.date.getTime()}
                  className={'cal-day is-in' + (sel === c.index ? ' is-sel' : '')}
                  data-iv={c.index}
                  aria-pressed={sel === c.index}
                  title={tip(b, 'day')}
                  onClick={() => onPick(c.index)}
                  style={{ background: lv < 0 ? 'var(--heat-none)' : `var(--heat-${lv})` }}
                />
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}

/**
 * 切片井：点一格展开，答「这一段是怎么构成的」——缓存 / 净输入 / 输出三段。
 *
 * 井里**不再列 by model / by key**：下面那张排行列表已经跟着切片走了，
 * 在这里再列一遍就是同一份数据画两遍（口径层 v0.60）。
 */
export function SliceWell({
  iv,
  unit,
  comp,
  onClose,
}: {
  iv: Interval
  unit: Unit
  comp: SliceComp
  onClose: () => void
}) {
  const seg = (v: number) => (comp.total ? (v / comp.total) * 100 : 0)
  const pct = (v: number) => (comp.total ? `${seg(v).toFixed(0)}%` : '—')
  return (
    <div className="slice">
      <div>
        <div className="slice-when">{whenLabel(iv, unit, true)}</div>
        <div className="slice-total">
          <b className="tnum" title={`${fmtInt(iv.tokens)} token`}>
            {fmtCompact(iv.tokens)}
          </b>
          <span className="muted">token</span>
        </div>
        <div className="slice-calls">
          {fmtInt(iv.calls)} 次调用
          {iv.errors > 0 && <span className="slice-bad"> · 失败 {fmtInt(iv.errors)}</span>}
        </div>
      </div>
      <div>
        <div className="comp-bar">
          <i className="comp-cache" style={{ width: `${seg(comp.cacheAll)}%` }} />
          <i className="comp-in" style={{ width: `${seg(comp.netIn)}%` }} />
          <i className="comp-out" style={{ width: `${seg(comp.out)}%` }} />
        </div>
        <div className="comp-legend">
          <div>
            <i className="comp-cache" />
            缓存 读 {fmtCompact(comp.cacheRead)} / 写 {fmtCompact(comp.cacheWrite)}
            <span className="tnum">{pct(comp.cacheAll)}</span>
          </div>
          <div>
            <i className="comp-in" />
            净输入 {fmtCompact(comp.netIn)}
            <span className="tnum">{pct(comp.netIn)}</span>
          </div>
          <div>
            <i className="comp-out" />
            输出 {fmtCompact(comp.out)}
            <span className="tnum">{pct(comp.out)}</span>
          </div>
        </div>
      </div>
      <button type="button" className="slice-x" aria-label="关掉这个切片" onClick={onClose}>
        ×
      </button>
    </div>
  )
}

/**
 * 环。段与段之间留 3 个路径单位的缝，段界不靠色差自己撑。
 *
 * **这一栏不带图例**（PO 2026-08-18 裁，口径层 v0.86 ⑧）：名字与百分比由紧挨着的
 * 「按模型」堆叠条给一次，同一屏里不重复三遍。
 */
export function Donut({ slices, total }: Composition) {
  const R = 62
  const C = 2 * Math.PI * R
  const GAP = 3.5
  let acc = 0
  return (
    <div className="donut-wrap">
      <svg viewBox="-76 -76 152 152" role="img" aria-label="按模型的 token 构成">
        <g transform="rotate(-90)">
          {slices.map((s) => {
            const share = s.tokens / total
            const len = Math.max(1, share * C - GAP)
            const el = (
              <circle
                key={s.label}
                r={R}
                cx={0}
                cy={0}
                fill="none"
                stroke={s.color}
                strokeWidth={20}
                strokeDasharray={`${len.toFixed(2)} ${(C - len).toFixed(2)}`}
                strokeDashoffset={(-acc).toFixed(2)}
              >
                <title>{`${s.label} · ${fmtInt(s.tokens)} token · ${((share * 100)).toFixed(1)}%`}</title>
              </circle>
            )
            acc += share * C
            return el
          })}
        </g>
      </svg>
      <div className="donut-mid">
        <b className="tnum">{fmtCompact(total)}</b>
        <span>token</span>
      </div>
    </div>
  )
}

/**
 * 占比堆叠条 + 图例。
 *
 * **每个系列必须直标名字 + 百分比**：这五个色对羊皮纸底有四个不足 3:1（实测 2.9 /
 * 2.55 / 1.96 / 2.44），dataviz 的 relief 条款靠直标满足，只摆色块不成立。
 */
export function Stack({ title, slices, total }: Composition & { title: string }) {
  return (
    <div>
      <div className="cat-title">{title}</div>
      <div className="stack">
        {slices.map((s) => (
          <i
            key={s.label}
            style={{ width: `${((s.tokens / total) * 100).toFixed(2)}%`, background: s.color }}
            title={`${s.label} · ${fmtInt(s.tokens)} token`}
          />
        ))}
      </div>
      <div className="cat-legend">
        {slices.map((s) => (
          <span key={s.label} title={`${s.label} · ${fmtInt(s.tokens)} token`}>
            <i style={{ background: s.color }} />
            <b>{s.label}</b>
            <em>{((s.tokens / total) * 100).toFixed(0)}%</em>
          </span>
        ))}
      </div>
    </div>
  )
}
