import { useMemo, useState } from 'react'
import { api } from '../api'
import type { UsageRow } from '../api'
import { Card, Empty, ErrorBar, fmtCompact, fmtInt, useList } from '../ui'
import { Segmented } from '../fields'
import { ModelIcon } from '../icons'
import { DAY_OPTIONS, sumUsage } from './usage-common'

// 聚合维度（v0.38 加了上游凭证，v0.53 加了 API Key）。
//
// 「按上游凭证」写全称：它聚合的是渠道下那些上游 key 的名字，跟你在这个网关里新建的
// API Key 是两回事，只写「按凭证」两边都像。
const DIM_OPTIONS = [
  { value: 'model' as const, label: '按模型' },
  { value: 'key' as const, label: '按 API Key' },
  { value: 'credential' as const, label: '按上游凭证' },
]

/** 一行行数按维度换个量词，别让「3 个模型」和「3 份凭证」长成同一句。 */
const DIM_UNIT: Record<string, string> = {
  model: '个模型',
  key: '把 API Key',
  credential: '份上游凭证',
}

/**
 * 排行：这段时间**谁在烧**（口径层 v0.58 定形态，v0.60 单独成页）。
 *
 * 取的只有 new-api 模型广场 / OpenRouter rankings 的那个**形态**：名次 + 图标 +
 * 名字 + 一个主指标 + 占比。参照页上另外三块——热门榜、厂商市场份额、↑8500% 那种
 * 环比——仍不做：那三块要的是分母或上一周期，这里两样都没有。占比是例外，它的分母
 * 就是这一页自己的合计，说的是自己这段时间的构成，不是「别人在用什么」。
 */
export default function Rankings() {
  const [days, setDays] = useState('7')
  const [dim, setDim] = useState('model')
  const usage = useList(
    () => api.get<{ days: number; rows: UsageRow[] | null }>(`/usage?days=${days}&by=${dim}`),
    [days, dim], // 天数或维度一变就重拉
  )

  const rows = usage.data?.rows ?? []
  const total = useMemo(() => sumUsage(rows), [rows])
  const totalTokens = total.input + total.output

  // 名次按 **token 总量** 排，不按后端那句 `ORDER BY COUNT(*)`：这一页问的是「谁在烧」，
  // 而调用次数与 token 能差一个量级（93 次烧 317 万，5 次烧 40 万，DESIGN v0.11 记过）。
  // 并列时退回调用次数——上游整段不报 usage 时所有行的 token 都是 0，那时次数是唯一还
  // 有区分度的数，名次至少不是随机的。排序放前端：行数是模型数量级，且同一个端点还给
  // 调用记录页那个模型下拉供选项，改 SQL 会把两处绑在一起。
  const ranked = useMemo(
    () =>
      [...rows].sort(
        (a, b) =>
          b.input_tokens + b.output_tokens - (a.input_tokens + a.output_tokens) ||
          b.calls - a.calls,
      ),
    [rows],
  )

  return (
    <>
      <ErrorBar message={usage.error} />
      <Card
        title="排行"
        action={
          <div className="rank-controls">
            <Segmented value={dim} options={DIM_OPTIONS} onChange={setDim} />
            <Segmented value={days} options={DAY_OPTIONS} onChange={setDays} />
          </div>
        }
      >
        {/* 只摆合计一个数，不重复概览页那条指标条（DESIGN §6 同一份数据不画两遍）：
            它在这里的身份是**下面每一行占比的分母**，没有它，「57.9%」没有出处。 */}
        <div className="stats">
          <div className="stat-lead">
            <span
              className="stat-lead-value"
              title={`输入 ${fmtInt(total.input)} · 输出 ${fmtInt(total.output)}`}
            >
              {fmtCompact(totalTokens)}
            </span>
            <span className="stat-lead-label">
              token 合计 · {rows.length} {DIM_UNIT[dim] ?? '个模型'}
            </span>
          </div>
        </div>

        {ranked.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <ol className="rank-list">
            {ranked.map((r, i) => {
              const t = r.input_tokens + r.output_tokens
              return (
                <li className="rank-row" key={r.label}>
                  {/* 名次单独一格、等宽右对齐：它是这份列表唯一的序，让它自己成一列，
                      1 和 10 的个位才对得齐。 */}
                  <span className="rank-no tnum">{i + 1}</span>
                  {/* 只有模型维度画图标：那个图标是从模型名猜厂商猜出来的，套在人自己
                      起的凭证名 / key 名上只会猜出一堆无意义的首字母块。 */}
                  {dim === 'model' ? <ModelIcon model={r.label} size={20} /> : null}
                  <div className="rank-main">
                    <code className="rank-label" title={r.label}>
                      {r.label}
                    </code>
                    {/* 原来那张表的六列压成一行附注：主指标只留 token 合计，其余退到这里。
                        缓存两项只在非零时露出——绝大多数上游根本不报，每行挂一对 0 是噪声。 */}
                    <div className="sub">
                      {fmtInt(r.calls)} 次调用
                      {r.errors > 0 && (
                        <>
                          {' · '}
                          <span className="rank-bad">失败 {fmtInt(r.errors)}</span>
                        </>
                      )}
                      {' · 入 '}
                      {fmtCompact(r.input_tokens)}
                      {' / 出 '}
                      {fmtCompact(r.output_tokens)}
                      {r.cache_read_tokens || r.cache_write_tokens
                        ? ` · 缓存 读 ${fmtCompact(r.cache_read_tokens)} / 写 ${fmtCompact(r.cache_write_tokens)}`
                        : ''}
                    </div>
                  </div>
                  {/* 缩写掉的精确值进 title：表没了之后，这是唯一还能拿到原数的地方。 */}
                  <div
                    className="rank-metric"
                    title={`输入 ${fmtInt(r.input_tokens)} · 输出 ${fmtInt(r.output_tokens)} · 缓存读 ${fmtInt(r.cache_read_tokens)} · 缓存写 ${fmtInt(r.cache_write_tokens)}`}
                  >
                    <span className="rank-metric-value tnum">{fmtCompact(t)}</span>
                    <span className="rank-metric-unit">token</span>
                    {/* 上游整段不报 usage 时全列都是 0，占比算出来是 0.0% 而不是「不知道」
                        ——那时写「—」，名次此刻靠的是调用次数。 */}
                    <div className="sub tnum">
                      {totalTokens ? `${((t / totalTokens) * 100).toFixed(1)}%` : '—'}
                    </div>
                  </div>
                </li>
              )
            })}
          </ol>
        )}
      </Card>
    </>
  )
}
