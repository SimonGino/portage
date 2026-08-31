import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../api'
import type { BucketUsage, UsageRow } from '../../api'
import { Card, Empty, ErrorBar, fmtCompact, fmtInt, fmtMoney, useList } from '../../ui'
import { Segmented } from '../../fields'
import { ModelIcon } from '../../icons'
import {
  buildIntervals,
  calendarWeeks,
  colorMap,
  composition,
  heatScale,
  intervalRange,
  ivStats,
  rankSort,
  sliceComposition,
  tokensOf,
  windowSpec,
} from './intervals'
import { Donut, SkyBars, SkyCalendar, SkyMatrix, SliceWell, Stack, md, whenLabel } from './sky'

/**
 * 统计窗口。「7 天」= 今天往前数 7 个自然日、按本地时区（口径层 v0.55），不是滚动
 * 7×24 小时。仍是 1 / 7 / 30 三档，不开自定义范围——`from`/`to` 是给下钻用的，
 * 不是给窗口选择器用的。
 *
 * 原先住在 `usage-common.ts`，那个文件是为「概览与排行共用」立的；概览页在口径层
 * v0.75 去掉、`sumUsage` 随之无人调用之后，那里只剩这一个常量和一个过期的名字。
 */
const DAY_OPTIONS = [
  { value: '1' as const, label: '1 天' },
  { value: '7' as const, label: '7 天' },
  { value: '30' as const, label: '30 天' },
]

// 聚合维度（v0.38 加了上游凭证，v0.53 加了 API Key）。
//
// 「按上游凭证」写全称：它聚合的是渠道下那些上游 key 的名字，跟你在这个网关里新建的
// API Key 是两回事，只写「按凭证」两边都像。
const DIM_OPTIONS = [
  { value: 'model' as const, label: '按模型' },
  { value: 'key' as const, label: '按 API Key' },
  { value: 'user' as const, label: '按用户' },
  { value: 'credential' as const, label: '按上游凭证' },
]

type Dim = (typeof DIM_OPTIONS)[number]['value']

/** 一行行数按维度换个量词，别让「3 个模型」和「3 份凭证」长成同一句。 */
const DIM_UNIT: Record<Dim, string> = {
  model: '个模型',
  key: '把 API Key',
  user: '位用户',
  credential: '份上游凭证',
}

/** 一个 scope 下的几个维度汇总。环与堆叠条恒要 model + key，排行列表要当前那个维度。 */
type DimRows = Partial<Record<Dim, UsageRow[]>>

async function fetchDims(base: string, query: string, dims: Dim[]): Promise<DimRows> {
  const out: DimRows = {}
  await Promise.all(
    dims.map(async (d) => {
      const r = await api.get<{ rows: UsageRow[] | null }>(`${base}?${query}&by=${d}`)
      out[d] = r.rows ?? []
    }),
  )
  return out
}

/**
 * 排行：**什么时候烧的 → 那一段是怎么构成的 → 谁在烧**，一条下钻链（口径层 v0.86）。
 *
 * 三层同一屏从上往下读；点中节律带上一格之后**三层共用一个 scope**——否则同一屏上
 * 会出现「上面是整窗、下面是这一小时」两套数。
 *
 * 取数分三路，各有各的 key：
 *   - `/usage/buckets` 只跟窗口档位有关，切维度不重取（节律带与那几个读数都吃它）。
 *   - `/usage?days=` 整窗聚合，色映射的出处；没选中区间时它也是三处共用的 scope。
 *   - `/usage?from=&to=` 选中那一格之后按那一格重算（#21 给的那两个参数）。
 *
 * `mine` 是「我的」空间「用量与配额」页的那份（PO 2026-08-31 拍板公共组件）：
 * 同一条下钻链、同一套图，数据换走 `/my/usage`（服务端把归属焊死在 WHERE 里）；
 * 维度只剩按模型 / 按 API Key——按用户在单人视角里没有意义，按上游凭证是运营
 * 细节，两档服务端本来就不开。排行附注多一截费用：配额按钱记账（口径层 §2.10），
 * 这一页顶上就是配额卡，「哪个模型烧了我多少钱」得答得上来。
 */
export default function RankingsView({ mine = false }: { mine?: boolean }) {
  const base = mine ? '/my/usage' : '/usage'
  const [days, setDays] = useState('7')
  const [dim, setDim] = useState<Dim>('model')
  const [sel, setSel] = useState<number | null>(null)

  // 环与两条堆叠条恒要 model + key（口径层 v0.86 ⑧），排行列表要当前维度。
  // 按凭证 / 按用户看时才多取一路，前两档不多发一次请求。
  const dims = useMemo<Dim[]>(
    () => (dim === 'model' || dim === 'key' ? ['model', 'key'] : ['model', 'key', dim]),
    [dim],
  )
  const dimsKey = dims.join(',')

  // 回包里带上这一发用的窗口档位与「现在」：在飞的那一刻拿新档位配旧数据铺格子，
  // 会用 24 行去铺 168 格、闪一帧空图。带着一起换，旧图就原样留到新数据落地。
  const buckets = useList(async () => {
    const n = Number(days)
    const { unit } = windowSpec(n)
    const r = await api.get<{ rows: BucketUsage[] | null }>(
      `${base}/buckets?days=${days}&unit=${unit}`,
    )
    return { days: n, rows: r.rows ?? [], now: new Date() }
  }, [days])

  const winUsage = useList(() => fetchDims(base, `days=${days}`, dims), [days, dimsKey])

  const { unit, list } = useMemo(
    () =>
      buckets.data
        ? buildIntervals(buckets.data.days, buckets.data.rows, buckets.data.now)
        : { unit: 'hour' as const, list: [] },
    [buckets.data],
  )

  const picked = sel != null ? (list[sel] ?? null) : null
  const range = picked ? intervalRange(picked) : null
  const sliceKey = range ? `${range.from}-${range.to}` : ''
  // 回包带上自己那一段的 key：手上这份到底说的是不是当前选中的这一格，只能这么认。
  const sliceUsage = useList(
    () =>
      range
        ? fetchDims(base, `from=${range.from}&to=${range.to}`, dims).then((rows) => ({
            key: `${range.from}-${range.to}`,
            rows,
          }))
        : Promise.resolve(null),
    [sliceKey, dimsKey],
  )

  // 选中之后三处（环、堆叠条、排行列表）看同一个 scope。**这一段没到手就什么都不说**，
  // 不拿整窗的数顶上：那会让同一屏出现「上面是这一小时、下面是整窗」两套数，而下面那句
  // 还写着「只看选中的那个区间」——正是口径层 v0.86 ③ 要避的。取失败时这个顶替还不会
  // 自己消失，一直挂着一份错的。没选中时整窗慢半拍不要紧：旧窗口的数与旧标签本就自洽，
  // 三档窗口切换不闪那条要的就是这个。
  const scope: DimRows | null = picked
    ? sliceUsage.data?.key === sliceKey
      ? sliceUsage.data.rows
      : null
    : (winUsage.data ?? null)
  /** 不知道就写不知道，别把别处的数摆在这一段的标签底下。 */
  const scopeText = sliceUsage.loading ? '正在取这一段…' : '没取到这一段的构成。'

  const stats = useMemo(() => ivStats(list), [list])
  const scale = useMemo(() => heatScale(list), [list])
  const cal = useMemo(
    () => (unit === 'day' && buckets.data ? calendarWeeks(list, buckets.data.now) : null),
    [unit, list, buckets.data],
  )

  // 色跟着**实体**走不跟名次走：整窗聚合算一次映射，之后只查表。切一个区间名次会变，
  // 按名次上色会让整排条同时改色，「蓝色那条」在两次点击之间就不是同一个东西了。
  const colors = useMemo(
    () => ({
      model: colorMap(winUsage.data?.model ?? []),
      key: colorMap(winUsage.data?.key ?? []),
    }),
    [winUsage.data],
  )
  const byModel = useMemo(() => composition(scope?.model ?? [], colors.model), [scope, colors])
  const byKey = useMemo(() => composition(scope?.key ?? [], colors.key), [scope, colors])

  const ranked = useMemo(() => rankSort(scope?.[dim] ?? []), [scope, dim])
  // 这一页只有这一个合计，它同时是下面每一行占比的**分母**（DESIGN §5.2 ⑥），
  // 所以它跟着 scope 走：选中一格之后它说的就是那一格。
  const rankTotal = ranked.reduce((a, r) => a + tokensOf(r), 0)

  /** 换窗口时旧下标指向另一格，必须清掉；换维度不清——问的还是同一段时间。 */
  function pickDays(v: string) {
    setSel(null)
    setDays(v)
  }

  /** 再点一次同一格就是取消。 */
  const pick = (i: number) => setSel((cur) => (cur === i ? null : i))

  useEffect(() => {
    if (sel == null) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setSel(null)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [sel])

  // 尖角指向被选中那一格。按窗口各推一套公式（列宽 × 下标）要写三份，且日历那版的
  // 列宽还跟着 --cell 变；渲染完直接量一次省事也不会错。
  const skyRef = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    const root = skyRef.current
    const cell = root?.querySelector<HTMLElement>('[data-iv].is-sel')
    const well = root?.querySelector<HTMLElement>('.slice')
    if (!cell || !well) return
    const a = cell.getBoundingClientRect()
    const b = well.getBoundingClientRect()
    // 夹在井两端之内：选中最左 / 最右那格时，尖角不该跑到圆角外面去。
    well.style.setProperty(
      '--caret',
      `${Math.max(16, Math.min(b.width - 28, a.left + a.width / 2 - b.left - 6))}px`,
    )
  })

  const winDays = buckets.data?.days ?? Number(days)
  const unitWord = unit === 'hour' ? '小时' : '天'
  /** 量词跟着粒度换：「30 个天」不是中文，「154 个小时」才是。 */
  const countWord = (n: number) => (unit === 'hour' ? `${n} 个小时` : `${n} 天`)
  const first = list[0]
  const last = list[list.length - 1]
  const rangeText = !first
    ? ''
    : winDays === 1
      ? `${md(first.start)} 00:00 起的 24 小时`
      : `${md(first.start)} – ${md(last.start)}`
  const scopeWord = picked ? whenLabel(picked, unit, true) : `这 ${winDays} 天`
  // 柱靠高度说话，其余两档靠深浅；色阶图例只在后者出现。
  const usesHeat = winDays !== 1

  return (
    <>
      <ErrorBar message={buckets.error || winUsage.error || sliceUsage.error} />
      <Card
        title={mine ? '用量' : '排行'}
        action={<Segmented value={days} options={DAY_OPTIONS} onChange={pickDays} />}
      >
        {/* 只摆合计一个数，不重复下面那排读数（DESIGN §6 同一份数据不画两遍）：
            它在这里的身份是**每一行占比的分母**，没有它，「57.9%」没有出处。 */}
        <div className="stats">
          <div className="stat-lead">
            <span className="stat-lead-value" title={scope ? fmtInt(rankTotal) : ''}>
              {scope ? fmtCompact(rankTotal) : '—'}
            </span>
            <span className="stat-lead-label">
              token 合计 · {scopeWord}
              {scope ? ` · ${ranked.length} ${DIM_UNIT[dim]}` : ''}
            </span>
          </div>
        </div>

        <section className="sky" aria-label="节律" ref={skyRef}>
          <div className="sky-head">
            <div>
              {rangeText} · 一格 = 1 {unitWord}
            </div>
            {usesHeat && (
              <div className="sky-legend">
                <span>少</span>
                {[1, 2, 3, 4].map((l) => (
                  <i key={l} style={{ background: `var(--heat-${l})` }} />
                ))}
                <span>多</span>
              </div>
            )}
          </div>

          {/* 环在左、节律在右，三档窗口都走这个两栏。环这一栏**不带图例**（PO
              2026-08-18 裁）：名字与百分比由下面那条「按模型」堆叠条给一次。 */}
          <div className="sky-split">
            <div>
              {!scope ? (
                <p className="sky-hint">{scopeText}</p>
              ) : byModel.total > 0 ? (
                <Donut slices={byModel.slices} total={byModel.total} />
              ) : (
                <p className="sky-hint">这段时间还没有调用。</p>
              )}
            </div>
            {/* 头一次渲染时桶还没到，list 是空的：三种排布一律等数据落地再画，
                别让点阵去切一个空数组（切出来是七个空行，取 row[0] 就炸）。 */}
            <div>
              {list.length === 0 ? null : winDays === 1 ? (
                <SkyBars list={list} sel={sel} onPick={pick} />
              ) : unit === 'hour' ? (
                <SkyMatrix list={list} scale={scale} sel={sel} onPick={pick} />
              ) : cal ? (
                <SkyCalendar
                  list={list}
                  scale={scale}
                  sel={sel}
                  onPick={pick}
                  weeks={cal.weeks}
                  months={cal.months}
                />
              ) : null}
            </div>
          </div>

          <p className="sky-hint">点一格，看那一段是谁在烧。</p>
          {picked && (
            <SliceWell
              iv={picked}
              unit={unit}
              comp={sliceComposition(picked)}
              onClose={() => setSel(null)}
            />
          )}

          {/* 参照图底下那两条。「BY MEMBER」在单人网关里没有对应物，最近的是
              「按 API Key」——它答的是「哪个客户端在烧」。 */}
          {byModel.total > 0 && (
            <div className="breakdown">
              <Stack title="按模型" slices={byModel.slices} total={byModel.total} />
              <Stack title="按 API Key" slices={byKey.slices} total={byKey.total} />
            </div>
          )}

          {/* 节律带的几个读数，全部出自同一个桶数组，一套文案吃三档窗口。
              **分母只数已经过去的区间**：今天 15 点看「1 天」是 N/16 不是 N/24。
              合计不在这里再报一遍，它在上面那条指标行里当占比的分母。 */}
          <dl className="ivstats">
            <div className="ivstat">
              <dt>每{unitWord}均值</dt>
              <dd>{fmtCompact(stats.avg)}</dd>
            </div>
            <div className="ivstat">
              <dt>峰值区间</dt>
              <dd>
                {stats.peak ? fmtCompact(stats.peak.tokens) : '—'}
                <small>{stats.peak ? whenLabel(stats.peak, unit) : '整段没有报出 token'}</small>
              </dd>
            </div>
            <div className="ivstat">
              <dt>活跃区间</dt>
              <dd>
                {stats.activeN}
                <small>共 {countWord(stats.n)}</small>
              </dd>
            </div>
            <div className="ivstat">
              <dt>最长连续</dt>
              <dd>
                {stats.streak}
                <small>连着 {countWord(stats.streak)}有调用</small>
              </dd>
            </div>
          </dl>
        </section>

        <div className="rank-scope">
          <div className="rank-scope-note">
            {picked ? '只看选中的那个区间。再点一次或按 Esc 回到整窗。' : `这 ${winDays} 天全部。`}
          </div>
          {/* 用户侧只剩前两档：按用户在单人视角里没有意义，按上游凭证是运营细节，
              /my/usage 对这两档一律当默认处理（服务端 myUsage 的注释）。 */}
          <Segmented
            value={dim}
            options={mine ? DIM_OPTIONS.slice(0, 2) : DIM_OPTIONS}
            onChange={setDim}
          />
        </div>

        {!scope ? (
          <Empty>{scopeText}</Empty>
        ) : ranked.length === 0 ? (
          <Empty>这段时间还没有调用。</Empty>
        ) : (
          <ol className="rank-list">
            {ranked.map((r, i) => {
              const t = tokensOf(r)
              return (
                // key 名自 #73 起按用户唯一而非全局唯一，裸 label 会撞；带上归属才是
                // 这一行的自然键（其余维度 user 恒空，退化回原样）。
                <li className="rank-row" key={`${r.user ?? ''} ${r.label}`}>
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
                      {/* 按 key 看时把归属摆进附注（#75）：两个人各有一把「笔记本」
                          是常态，不带归属这两行分不出彼此。本人视角不摆——每行的
                          归属都是自己。 */}
                      {!mine && r.user ? `${r.user} · ` : ''}
                      {fmtInt(r.calls)} 次调用
                      {/* 费用只上用户侧（配额按钱记账，这页顶上就是配额卡）；管理端
                          排行的形态多轮拍板过，不顺手动。 */}
                      {mine && r.cost_usd > 0 ? ` · ${fmtMoney(r.cost_usd)}` : ''}
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
                      {rankTotal ? `${((t / rankTotal) * 100).toFixed(1)}%` : '—'}
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
