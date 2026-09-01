import { useState } from 'react'
import { api } from '../../api'
import type { ChannelModel, PricingModelPrice } from '../../api'
import { IconPencil } from '../../icons/acts'
import { PRICE_FIELDS, fmtPrice } from '../../prices'

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

  const current: Record<string, number | null> = {
    input: model.price_input,
    output: model.price_output,
    cache_read: model.price_cache_read,
    cache_write: model.price_cache_write,
  }
  const unpriced = PRICE_FIELDS.every(([k]) => current[k] === null)

  function put(prices: Record<string, number | null>) {
    void mutate(() => api.put(`/channel-models/${model.id}`, { disabled: model.disabled, prices }))
  }

  function save() {
    setEditing(false)
    const next: Record<string, number | null> = {}
    for (const [k] of PRICE_FIELDS) {
      const raw = (draft[k] ?? '').trim()
      if (raw === '') {
        next[k] = null
        continue
      }
      const n = Number(raw)
      // 解析不出或负数整组不存（同上限那颗的处置）：四价是一笔整组覆盖，
      // 存下能解析的那几个会把没看清的输入悄悄写成 null。
      if (!Number.isFinite(n) || n < 0) return
      next[k] = n
    }
    if (PRICE_FIELDS.every(([k]) => next[k] === current[k])) return
    put(next)
  }

  function open() {
    setDraft(
      Object.fromEntries(PRICE_FIELDS.map(([k]) => [k, current[k] === null ? '' : String(current[k])])),
    )
    setEditing(true)
  }

  const title = PRICE_FIELDS.map(([k, label]) => `${label} ${fmtPrice(current[k])}`).join('，')

  // 建议与现值逐项相等就不摆「采纳」：快照缺的价按 null 比，别拿 0 充数。
  const suggestDiffers =
    suggest !== null && PRICE_FIELDS.some(([k]) => (suggest[k] ?? null) !== current[k])
  const suggestChip = suggestDiffers && (
    <button
      type="button"
      className="chip-toggle chip-suggest"
      title={`models.dev 快照的建议价（USD/百万 token）：${PRICE_FIELDS.map(
        ([k, label]) => `${label} ${fmtPrice(suggest![k])}`,
      ).join('，')}。只是建议，点「采纳」才落库`}
      onClick={() =>
        put(Object.fromEntries(PRICE_FIELDS.map(([k]) => [k, suggest![k] ?? null])))
      }
    >
      models.dev {fmtPrice(suggest!.input)}/{fmtPrice(suggest!.output)} · 采纳
    </button>
  )

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
              <input
                autoFocus={i === 0}
                value={draft[k] ?? ''}
                inputMode="decimal"
                onChange={(e) => setDraft((d) => ({ ...d, [k]: e.target.value.replace(/[^0-9.]/g, '') }))}
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
            </span>
          ))}
          <span className="limit-edit-unit price-edit-unit">$/M</span>
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
        title={`单价（USD/百万 token）：${title}。点击修改；改价只影响之后的流水，不追溯`}
      >
        {fmtPrice(current.input)}/{fmtPrice(current.output)}
        <IconPencil />
      </button>
      {(current.cache_read !== null || current.cache_write !== null) && (
        <span className="muted">
          缓存 {fmtPrice(current.cache_read)}/{fmtPrice(current.cache_write)}
        </span>
      )}
      {suggestChip}
    </div>
  )
}
