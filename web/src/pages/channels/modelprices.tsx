import { useState } from 'react'
import { api } from '../../api'
import type { ChannelModel, PricingModelPrice } from '../../api'
import { IconPencil } from '../../icons/acts'
import {
  PRICE_FIELDS,
  TIER_FIELDS,
  fmtPrice,
  fmtTierTitle,
  fmtTokensShort,
  modelPriceBody,
  parsePriceDraft,
  samePrices,
  suggestPriceBody,
} from '../../prices'
import type { PriceBody } from '../../prices'

/**
 * ModelPrices 是纳管条目的「定价」编辑件（口径层 §2.10，#74；DESIGN v0.41 收进
 * v0.38 那副胶囊家族）。三态同一副身形：未定价 = 虚线胶囊「+ 定价」，定了 = 实底
 * 芯片「$入/$出」悬停浮出铅笔（四价全文在 title）；编辑是胶囊输入组——四价各一个
 * （数字与「$/M」单位同框），焦点离开整组或回车即存，Esc 丢弃。
 * **空 = 清回未定价（null），0 = 真免费**，两态别抹成一个。
 *
 * 「未定价」提醒的判据是**四价全 null 且有用量**：没人用过的条目不催着定价，
 * 用过的未定价条目每一笔 cost 都在记 0，钱正在悄悄漏。
 *
 * 建议价来自内置 models.dev 快照（渠道标注了 provider 才有），chip-suggest 同
 * 协议子集那颗「采纳」的形制：只提示，点了才落库；快照缺哪一价就建议 null，不补 0。
 *
 * 分档价（v0.75，#185）：有分档时实底芯片后跟小字「· >200k ↑」、全文进 title；编辑态
 * 四框之下一颗默认收起的「+ 分档」，展开为「超过 [N] token 后」+ 四框，清空阈值 = 去分档。
 * 建议价带分档时 chip 后缀「>200k 另价」，采纳连分档一起落。
 *
 * v0.62 起渠道详情页与定价页总表共用这一个组件（PO 2026-09-01 裁定总表可编辑，
 * 推翻 v1.10「只读总表」）——编辑面还是同一副，只是摆进了两页。
 */
export function ModelPrices({
  model,
  suggest,
  mutate,
}: {
  model: ChannelModel
  /** models.dev 快照里这个模型的建议价。null = 没建议（没标注 provider / 快照里没有它）。 */
  suggest: PricingModelPrice | null
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [tierOpen, setTierOpen] = useState(false)

  const current = modelPriceBody(model)
  const unpriced = PRICE_FIELDS.every(([k]) => current[k] === null)

  function put(prices: PriceBody) {
    void mutate(() => api.put(`/channel-models/${model.id}`, { prices }))
  }

  function save() {
    setEditing(false)
    // 解析不出、负数、阈值不是正整数整组不存（同上限那颗的处置），见 parsePriceDraft。
    const next = parsePriceDraft(draft)
    if (next === null || samePrices(next, current)) return
    put(next)
  }

  function open() {
    setDraft(
      Object.fromEntries(Object.entries(current).map(([k, v]) => [k, v === null ? '' : String(v)])),
    )
    setTierOpen(current.tier_above !== null)
    setEditing(true)
  }

  const title = PRICE_FIELDS.map(([k, label]) => `${label} ${fmtPrice(current[k])}`).join('，')
  const tierTitle = current.tier_above !== null ? `；${fmtTierTitle(current)}` : ''
  const tierSuffix = current.tier_above !== null && (
    <span className="muted" title={fmtTierTitle(current)}>
      · &gt;{fmtTokensShort(current.tier_above)} ↑
    </span>
  )

  // 建议与现值逐项相等（连分档）就不摆「采纳」：快照缺的价按 null 比，别拿 0 充数。
  const suggested = suggest === null ? null : suggestPriceBody(suggest)
  const suggestChip = suggested !== null && !samePrices(suggested, current) && (
    <button
      type="button"
      className="chip-toggle chip-suggest"
      title={`models.dev 快照的建议价（USD/百万 token）：${PRICE_FIELDS.map(
        ([k, label]) => `${label} ${fmtPrice(suggested[k])}`,
      ).join('，')}${suggested.tier_above !== null ? `；${fmtTierTitle(suggested)}` : ''}。只是建议，点「采纳」才落库（连分档一起）`}
      onClick={() => put(suggested)}
    >
      models.dev {fmtPrice(suggested.input)}/{fmtPrice(suggested.output)}
      {suggested.tier_above !== null && (
        <span className="muted"> &gt;{fmtTokensShort(suggested.tier_above)} 另价</span>
      )}{' '}
      · 采纳
    </button>
  )

  function priceInput(k: string, autoFocus: boolean, digitsOnly = false) {
    return (
      <input
        autoFocus={autoFocus}
        value={draft[k] ?? ''}
        inputMode={digitsOnly ? 'numeric' : 'decimal'}
        onChange={(e) =>
          setDraft((d) => ({ ...d, [k]: e.target.value.replace(digitsOnly ? /[^0-9]/g : /[^0-9.]/g, '') }))
        }
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            save()
          } else if (e.key === 'Escape') {
            setEditing(false)
          }
        }}
        placeholder="—"
      />
    )
  }

  if (editing) {
    return (
      <div className="model-protocols">
        <span
          className="model-protocols-label"
          title="USD/百万 token 的四项单价。留空 = 未定价（有用量记 0 并提醒），0 = 真免费。改价只影响之后的流水，不追溯"
        >
          定价
        </span>
        <span
          className="price-edit-group"
          onBlur={(e) => {
            // 四个输入框共用一次保存：焦点还在组内（在往下一格挪）不算离开。
            if (e.relatedTarget instanceof Node && e.currentTarget.contains(e.relatedTarget)) return
            save()
          }}
        >
          {PRICE_FIELDS.map(([k, label], i) => (
            <span key={k} className="limit-edit price-edit">
              <span className="price-edit-label">{label}</span>
              {priceInput(k, i === 0)}
            </span>
          ))}
          <span className="limit-edit-unit price-edit-unit">$/M</span>
          {tierOpen ? (
            <span
              className="price-tier-row"
              title="分档价：一笔调用的毛输入（含缓存读写）超过这个 token 数，整笔四项全按下面的价计；某价留空 = 沿用基础价。清空阈值 = 去分档"
            >
              <span className="price-edit-label">超过</span>
              <span className="limit-edit price-edit price-tier-above">{priceInput('tier_above', true, true)}</span>
              <span className="price-edit-label">token 后</span>
              {TIER_FIELDS.map(([k, label]) => (
                <span key={k} className="limit-edit price-edit">
                  <span className="price-edit-label">{label}</span>
                  {priceInput(k, false)}
                </span>
              ))}
            </span>
          ) : (
            <span className="price-tier-row">
              <button
                type="button"
                className="price-tier-add"
                // mousedown 不抢焦点：Safari 点按钮不给它焦点，焦点一离开输入组就会先存盘收起。
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => setTierOpen(true)}
                title="长上下文分档价：毛输入超过阈值的调用整笔按另一套四价计"
              >
                + 分档
              </button>
            </span>
          )}
        </span>
        <span className="muted">空 = 未定价 · 0 = 免费</span>
      </div>
    )
  }

  if (unpriced) {
    return (
      <div className="model-protocols">
        <button
          type="button"
          className="chip-add"
          onClick={open}
          title="填这个条目的四项单价（USD/百万 token）。不填的话，有用量的调用成本一律记 0"
        >
          + 定价
        </button>
        {model.has_usage && (
          <span
            className="tag tag-warn"
            title="这个条目已经有带用量的流水，但四价都没填——那些调用的成本都记成了 0。填上价之后的流水才按价计，不追溯"
          >
            未定价
          </span>
        )}
        {suggestChip}
      </div>
    )
  }

  return (
    <div className="model-protocols">
      <button
        type="button"
        className="model-limit-chip"
        onClick={open}
        title={`单价（USD/百万 token）：${title}${tierTitle}。点击修改；改价只影响之后的流水，不追溯`}
      >
        {fmtPrice(current.input)}/{fmtPrice(current.output)}
        <IconPencil />
      </button>
      {tierSuffix}
      {(current.cache_read !== null || current.cache_write !== null) && (
        <span className="muted">
          缓存 {fmtPrice(current.cache_read)}/{fmtPrice(current.cache_write)}
        </span>
      )}
      {suggestChip}
    </div>
  )
}
