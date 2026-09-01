import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import type { Channel, ChannelModel, PricingModelPrice, PricingModels, PricingProvider } from '../api'
import { Card, Dialog, Empty, ErrorBar, Field, useList } from '../ui'
import { Segmented } from '../fields'
import { Picker } from '../fields'
import type { Option } from '../fields'
import { ChannelIcon, ModelIcon } from '../icons'
import { ModelPrices } from './channels/modelprices'
import { isUnpriced } from '../prices'

/**
 * 定价页（口径层 v1.10 立、v1.11 改可编辑；#81；DESIGN §5.4）：全渠道纳管模型 ×
 * 四价的平铺总表，答「每个纳管模型记什么价、谁还没定价」，并且**当场能改**——
 * 定价格就是渠道详情页那副 ModelPrices 胶囊（同一个组件，不是第二套编辑面），
 * 三态、采纳建议、未定价提醒全一致；「厂商标注」列行内可标（未标注的渠道标上
 * 才有建议价）。渠道格是文字链接，去详情页管协议、上限、批量填价那些渠道级的事。
 *
 * 未定价行（四价全 null 且**有用量**，口径层判据）恒置顶：用过的未定价条目每一笔
 * cost 都在记 0，钱正在悄悄漏；没人用过的只算「还没填」，不催。「只看未定价」筛的
 * 是四价全 null 的全部行——催的按用量分档，找的不分。
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
  /** 未定价且有用量：口径层的提醒判据，置顶。 */
  alert: boolean
}

/** 厂商标注的行内编辑（口径层 v1.11）：改的是渠道的 provider 标注——与「上游设置」
 *  弹框里那格是同一个字段，这里只是就地给一个入口，标上总表立刻有建议价。 */
function ProviderDialog({
  ch,
  onClose,
  onSaved,
}: {
  ch: Channel
  onClose: () => void
  onSaved: () => Promise<unknown>
}) {
  const providers = useList(() => api.get<PricingProvider[]>('/pricing/providers'))
  const [value, setValue] = useState(ch.provider)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const options = useMemo<Option<string>[]>(
    () => [
      { value: '', label: '未标注' },
      ...(providers.data ?? []).map((p) => ({ value: p.id, label: p.name })),
    ],
    [providers.data],
  )

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      // ChannelSettings 是指针语义：只带 name（必填）与 provider，其余字段不动。
      await api.put(`/channels/${ch.id}/settings`, { name: ch.name, provider: value })
      await onSaved()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <Dialog title={`厂商标注：${ch.name}`} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field
          label="厂商标注"
          hint="这个上游对应 models.dev 的哪一家，只用来出建议价与图标分组，不影响转发；中转站对不上就留「未标注」"
        >
          <Picker value={value} options={options} onChange={setValue} placeholder="未标注" />
        </Field>
        <ErrorBar message={error || providers.error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

export default function Pricing() {
  const channels = useList(() => api.get<Channel[] | null>('/channels'))
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<'all' | 'unpriced'>('all')
  const [annotating, setAnnotating] = useState<Channel | null>(null)
  // provider id → 人话名（302ai → 302 AI），标注列显名字——与身份条上那颗 tag
  // 同一副读法。拉失败就显 id，标注是可选项不为它挂错误条。
  const providerNames = useList(() => api.get<PricingProvider[]>('/pricing/providers').catch(() => []))
  // models.dev 建议价：按渠道标注过的 provider 各拉一次快照，喂给定价胶囊的
  // chip-suggest。拉失败当没有建议（快照是发版内置资产），不为它挂错误条。
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

  // 写操作统一走这一个口（同 AccessPoints 的成例）：失败上 ErrorBar，成功后整表
  // 重拉——定价胶囊与置顶排序都吃列表数据，改完就该看到新样子。
  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      channels.setError('')
    } catch (e) {
      channels.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await channels.reload()
  }

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
      {annotating && (
        <ProviderDialog
          ch={annotating}
          onClose={() => setAnnotating(null)}
          onSaved={() => channels.reload()}
        />
      )}
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
                <th>厂商标注</th>
                <th>定价（USD/百万 token）</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <tr key={r.m.id} className={r.m.disabled ? 'is-off' : ''}>
                  <td>
                    {/* 文字链接（§4 白名单②）：渠道级的事（协议、上限、批量填价）
                        还在详情页，这格就是过去的门。 */}
                    <Link className="pricing-ch" to={`/channels/${r.ch.id}`} title={`去「${r.ch.name}」详情页`}>
                      <ChannelIcon channel={r.ch} size={14} />
                      {r.ch.name}
                    </Link>
                  </td>
                  <td>
                    <span className="chip">
                      <ModelIcon model={r.m.upstream_model} size={14} />
                      <code>{r.m.upstream_model}</code>
                    </span>
                  </td>
                  <td>
                    {/* 同一颗钮管两态：已标注显名字（点了换），未标注是虚线「+ 标注」
                        ——标上这行立刻有建议价可采。 */}
                    {r.ch.provider ? (
                      <button
                        type="button"
                        className="chip-toggle"
                        title={`models.dev 标注 · ${r.ch.provider}。点击修改`}
                        onClick={() => setAnnotating(r.ch)}
                      >
                        {providerNames.data?.find((p) => p.id === r.ch.provider)?.name ?? r.ch.provider}
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="chip-add"
                        title="标注这个渠道对应 models.dev 的哪一家，标上才有建议价可采纳"
                        onClick={() => setAnnotating(r.ch)}
                      >
                        + 标注
                      </button>
                    )}
                  </td>
                  <td>
                    <ModelPrices
                      model={r.m}
                      suggest={r.ch.provider ? (suggested[r.ch.provider]?.[r.m.upstream_model] ?? null) : null}
                      mutate={mutate}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </>
  )
}
