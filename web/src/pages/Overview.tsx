import { useMemo, useState } from 'react'
import { api } from '../api'
import type { DailyUsage, UsageRow } from '../api'
import { Card, Empty, ErrorBar, fmtCompact, fmtInt, useList } from '../ui'
import { Segmented } from '../fields'
import { DAY_OPTIONS, sumUsage } from './usage-common'

const tokensOfDay = (d: DailyUsage) => d.input_tokens + d.output_tokens

/** 横轴最多摆几个日期标签。30 根柱子每根都标，横轴会糊成一条黑线。 */
const AXIS_LABELS = 8

/** 8/13 这种短日期：横轴上要回答的是「哪天」，年份与前导零都是噪音。 */
function fmtDay(day: string) {
  const [, m, d] = day.split('-')
  return `${Number(m)}/${Number(d)}`
}

/**
 * 按天的堆叠柱状图：一根柱子一天，下段输入、上段输出，高度按 token 总量相对最大值。
 *
 * 横轴是**时间**而不是模型（DESIGN v0.14）：按模型排名的那一版画的是「谁在烧」，
 * 而那正是排行页逐行说的事——图只是把同一份数据又画了一遍。时间是别处答不了的
 * 问题：什么时候烧的、在涨还是在落。
 *
 * 只在有 token 可画时出现。绝大多数上游会报 usage，但 sub2api 这类中转有时整段
 * 不报——那时全部天都是 0，画出来是一排贴地的横线，不如不画：一张说不出话的图
 * 比没有图更浪费那 200px。
 */
function UsageChart({ days }: { days: DailyUsage[] }) {
  const max = Math.max(...days.map(tokensOfDay), 0)
  if (max === 0) return null
  const total = days.reduce((a, d) => a + tokensOfDay(d), 0) || 1
  const peak = days.reduce((a, d) => (tokensOfDay(d) > tokensOfDay(a) ? d : a), days[0])
  const step = Math.ceil(days.length / AXIS_LABELS)

  return (
    <>
      <div className="usage-chart">
        {days.map((d, i) => {
          const t = tokensOfDay(d)
          const outShare = t ? (d.output_tokens / t) * 100 : 0
          return (
            <div className="usage-col" key={d.day}>
              {/* 0.6% 是给「这天有调用但一个 token 都没报」留的一线，让它仍占一格
                  位置。没有调用的那天则是真的 0——那天空着是实话，不该也画一条。 */}
              <div
                className="usage-col-stack"
                style={{ height: `${Math.max((t / max) * 100, d.calls > 0 ? 0.6 : 0)}%` }}
                title={`${d.day}：${fmtInt(d.calls)} 次调用 · 输入 ${fmtInt(d.input_tokens)} · 输出 ${fmtInt(d.output_tokens)}`}
              >
                <div className="usage-seg-out" style={{ height: `${outShare}%` }} />
                <div className="usage-seg-in" style={{ height: `${100 - outShare}%` }} />
              </div>
              {/* 标签隔着摆，柱子照样一根不少：横轴密到读不出来时，该少的是标签。 */}
              <div className="usage-col-label" title={d.day}>
                {i % step === 0 || i === days.length - 1 ? fmtDay(d.day) : ''}
              </div>
            </div>
          )
        })}
      </div>
      {/* 图例 + 一句「该看什么」（DESIGN.md §6）。堆叠的两段没法直接标注在柱子上
          （细柱塞不下两个数），这正是 §6 允许「系列区分不开时才用色」的那种情况；
          但光有图例就落进 §8 那条「图例代替直接标注」，所以把结论写出来——一张图
          该说的是「哪天最重」，不是「这里有两种颜色」。 */}
      <div className="usage-legend">
        <span>
          <i className="usage-dot" style={{ background: 'var(--data-in)' }} />
          输入
        </span>
        <span>
          <i className="usage-dot" style={{ background: 'var(--data-out)' }} />
          输出
        </span>
        <span>
          最重的一天是 <code>{fmtDay(peak.day)}</code>，占这 {days.length} 天的{' '}
          {((tokensOfDay(peak) / total) * 100).toFixed(1)}%
        </span>
        {/* 说出来，否则每次看最后一根都比前一根矮，会被读成「在掉」。 */}
        <span className="muted">最后一根是今天，还没走完</span>
      </div>
    </>
  )
}

/**
 * 概览：这段时间**烧了多少、什么时候烧的**（口径层 v0.60 三分之一）。
 *
 * 「谁在烧」在排行页，「刚才那几次怎么样」在调用记录页——三页各答一问，同一份数据
 * 不在两页上各说一遍（DESIGN §6）。所以这一页只有指标条与按天的柱状图，没有逐行明细。
 */
export default function Overview() {
  const [days, setDays] = useState('7')
  // 指标条按模型聚合取数：这一页不给维度开关（那是排行页的事），但合计与「几个模型」
  // 都得有个出处，而按哪个维度聚合都不改合计——换维度只改分组，不改总量。
  const usage = useList(
    () => api.get<{ days: number; rows: UsageRow[] | null }>(`/usage?days=${days}&by=model`),
    [days],
  )
  const daily = useList(
    () => api.get<{ days: number; rows: DailyUsage[] | null }>(`/usage/daily?days=${days}`),
    [days],
  )

  const rows = usage.data?.rows ?? []
  const total = useMemo(() => sumUsage(rows), [rows])
  const errRate = total.calls ? (total.errors / total.calls) * 100 : 0

  return (
    <>
      <ErrorBar message={usage.error || daily.error} />
      <Card
        title="概览"
        action={<Segmented value={days} options={DAY_OPTIONS} onChange={setDays} />}
      >
        {/* 一条有主次的指标行，不是三张等大卡（DESIGN.md §8）。调用是主语，失败次之
            且只有非零才上错误色，token 合计退成右边的附注。 */}
        <div className="stats">
          <div className="stat-lead">
            <span className="stat-lead-value">{fmtInt(total.calls)}</span>
            <span className="stat-lead-label">次调用 · {rows.length} 个模型</span>
          </div>
          <div className={'stat-side' + (total.errors > 0 ? ' is-bad' : '')}>
            失败 <b>{fmtInt(total.errors)}</b>
            <span>{total.calls ? `${errRate.toFixed(1)}%` : '—'}</span>
          </div>
          <div
            className="stat-tokens"
            title={`输入 ${fmtInt(total.input)} · 输出 ${fmtInt(total.output)} · 缓存读 ${fmtInt(total.cacheRead)} · 缓存写 ${fmtInt(total.cacheWrite)}`}
          >
            <span>
              合计 <b>{fmtCompact(total.input + total.output)}</b> token
              {total.cacheRead > 0 && `（缓存读 ${fmtCompact(total.cacheRead)}）`}
            </span>
          </div>
        </div>

        <UsageChart days={daily.data?.rows ?? []} />

        {/* 图在整段没有 token 时自己隐掉，那时这一页除了一行零就没别的了，说一句。 */}
        {total.calls === 0 && <Empty>这段时间还没有调用。</Empty>}
      </Card>
    </>
  )
}
