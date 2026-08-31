import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import type { Channel, ChannelModel, PricingModelPrice, PricingModels } from '../api'
import { Card, Empty, ErrorBar, useList } from '../ui'
import { Segmented } from '../fields'
import { ChannelIcon, ModelIcon } from '../icons'
import { fmtFourTitle, fmtPrice, isUnpriced } from '../prices'
import type { FourPrices } from '../prices'

/**
 * 定价页（口径层 v1.10，#81；DESIGN §5.4）：全渠道纳管模型 × 四价的**平铺只读
 * 总表**，答「每个纳管模型记什么价、谁还没定价」。这一页没有编辑面——定价输入组、
 * 「采纳」、批量填价都只住在渠道详情页那一处，点行跳过去改；总表不重复造第二套
 * 输入组（两套必然漂移）。
 *
 * 未定价行（四价全 null 且**有用量**，口径层判据）恒置顶并标 tag-warn：用过的
 * 未定价条目每一笔 cost 都在记 0，钱正在悄悄漏；没人用过的只算「还没填」，不催。
 * 「只看未定价」筛的是四价全 null 的全部行——催的按用量分档，找的不分。
 */

const PRICING_FILTERS = [
  { value: 'all' as const, label: '全部' },
  { value: 'unpriced' as const, label: '只看未定价' },
]

interface Row {
  ch: Channel
  m: ChannelModel
  /** 四价全 null——「未定价」，与真免费（0）两态分开。 */
  unpriced: boolean
  /** 未定价且有用量：口径层的提醒判据，置顶 + 警示色。 */
  alert: boolean
}

/** 单价面走 sans + tabular-nums，不套等宽——DESIGN §3 的 mono 白名单只有标识符，
 *  §5.1 既有的定价芯片也是这一副字形，别在总表上分叉。 */
function priceCell(m: ChannelModel) {
  const four: FourPrices = {
    input: m.price_input,
    output: m.price_output,
    cache_read: m.price_cache_read,
    cache_write: m.price_cache_write,
  }
  return (
    <span title={fmtFourTitle(four)}>
      {fmtPrice(m.price_input)}/{fmtPrice(m.price_output)}
      {(m.price_cache_read !== null || m.price_cache_write !== null) && (
        <span className="muted pricing-cache">
          {' '}
          缓存 {fmtPrice(m.price_cache_read)}/{fmtPrice(m.price_cache_write)}
        </span>
      )}
    </span>
  )
}

export default function Pricing() {
  const channels = useList(() => api.get<Channel[] | null>('/channels'))
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<'all' | 'unpriced'>('all')
  // models.dev 建议价对照列：按渠道标注过的 provider 各拉一次快照。纯对照——采纳
  // 回渠道详情页做。拉失败当没有建议（快照是发版内置资产），不为它挂错误条。
  const [suggested, setSuggested] = useState<Record<string, Record<string, PricingModelPrice>>>({})
  const providers = useMemo(
    () => [...new Set((channels.data ?? []).map((c) => c.provider).filter(Boolean))],
    [channels.data],
  )
  useEffect(() => {
    let gone = false
    for (const p of providers) {
      if (suggested[p]) continue
      api
        .get<PricingModels>(`/pricing/models?provider=${encodeURIComponent(p)}`)
        .then((r) => {
          if (!gone) setSuggested((s) => ({ ...s, [p]: r.models }))
        })
        .catch(() => {})
    }
    return () => {
      gone = true
    }
  }, [providers, suggested])

  const rows = useMemo(() => {
    const out: Row[] = []
    for (const ch of channels.data ?? []) {
      for (const m of ch.models ?? []) {
        const unpriced = isUnpriced({
          input: m.price_input,
          output: m.price_output,
          cache_read: m.price_cache_read,
          cache_write: m.price_cache_write,
        })
        out.push({ ch, m, unpriced, alert: unpriced && m.has_usage })
      }
    }
    // 置顶只提「未定价且有用量」那一档，其余保持渠道 id → 条目 id 的接入先后序
    //（sort 稳定，能这么省）。
    out.sort((a, b) => Number(b.alert) - Number(a.alert))
    return out
  }, [channels.data])

  if (channels.loading && channels.data === null) return <div className="boot">加载中…</div>

  const needle = q.trim().toLowerCase()
  const shown = rows.filter(
    (r) =>
      (filter === 'all' || r.unpriced) &&
      (needle === '' ||
        r.ch.name.toLowerCase().includes(needle) ||
        r.m.upstream_model.toLowerCase().includes(needle)),
  )

  return (
    <>
      <ErrorBar message={channels.error} />
      <Card
        title="定价"
        action={
          <div className="row-actions">
            <input
              className="pricing-search"
              placeholder="搜渠道或模型"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            <Segmented value={filter} options={PRICING_FILTERS} onChange={setFilter} />
          </div>
        }
      >
        {rows.length === 0 ? (
          <Empty>还没有渠道纳管模型。先去「模型」页接一家上游、纳管几个模型，价才有落处。</Empty>
        ) : shown.length === 0 ? (
          <Empty>{filter === 'unpriced' && needle === '' ? '每一条都定过价了。' : '没有匹配的条目。'}</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>渠道</th>
                <th>模型</th>
                <th>定价（USD/百万 token）</th>
                <th>models.dev 建议</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => {
                const sugg = r.ch.provider ? (suggested[r.ch.provider]?.[r.m.upstream_model] ?? null) : null
                return (
                  <tr
                    key={r.m.id}
                    className={'pricing-row' + (r.m.disabled ? ' is-off' : '')}
                    title={`去「${r.ch.name}」的详情页改价`}
                    onClick={() => navigate(`/channels/${r.ch.id}`)}
                  >
                    <td>
                      <span className="chip">
                        <ChannelIcon channel={r.ch} size={14} />
                        {r.ch.name}
                      </span>
                    </td>
                    <td>
                      <span className="chip">
                        <ModelIcon model={r.m.upstream_model} size={14} />
                        <code>{r.m.upstream_model}</code>
                      </span>
                    </td>
                    <td>
                      {r.unpriced ? (
                        <span
                          className={r.alert ? 'tag tag-warn' : 'muted'}
                          title={
                            r.alert
                              ? '有用量但四价全空，每一笔成本都在记 0——去渠道详情页填价'
                              : '四价全空（未定价）。还没有用量，不急'
                          }
                        >
                          未定价
                        </span>
                      ) : (
                        priceCell(r.m)
                      )}
                    </td>
                    <td className="muted">
                      {sugg ? (
                        <span title={fmtFourTitle(sugg) + '。只是对照，采纳去渠道详情页'}>
                          {fmtPrice(sugg.input)}/{fmtPrice(sugg.output)}
                        </span>
                      ) : (
                        '—'
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>
    </>
  )
}
