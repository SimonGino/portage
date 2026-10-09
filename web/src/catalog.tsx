/**
 * 模型目录（DESIGN v0.76，#189；口径层 v1.50）：两空间共用一个组件、两份数据接口。
 * 行 = 图标 + 名（可复制）+ 类型副行（接入点 / 直连）+ 能力胶囊：协议子集、输入上限
 * （条目已设实底；未设而快照有 limit.context 时虚线灰字「≈N · models.dev」只提示）、
 * 图片小图标（快照 modalities.input 含 image）、单价（$入/$出 + 分档后缀，未定价照实）。
 *
 * 胶囊里来自 models.dev 的部分是上游的静态自称，只做展示，不是能力位、不参与路由与
 * 拦截。不做「复制全部 ID」。
 */

import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { CatalogModel } from './api'
import { CopyIconButton, Empty, fmtInt } from './ui'
import { ModelIcon } from './icons'
import { fmtFourTitle, fmtPrice, fmtTierTitle, fmtTokensShort, isUnpriced, type FourPrices } from './prices'

/** 协议短名：目录胶囊只摆子集，全称进 title。 */
const PROTO_SHORT: Record<string, string> = {
  openai: 'Chat',
  openai_responses: 'Responses',
  anthropic: 'Anthropic',
}

/** 图片模态小图标（快照说这个模型吃图）：内联 SVG，不进 icons 资产表——它不是渠道/模型身份。 */
function ImageIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden>
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="5.5" cy="6.5" r="1.3" fill="currentColor" />
      <path d="M2.5 12l3.7-3.7 2.4 2.4 2.3-2.3 3.6 3.6" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}

/** 一行的胶囊描述（纯函数，测试直接吃这个）：kind 只影响样式与 title。 */
export interface CatalogPills {
  protocols: { label: string; title: string }[]
  /** 实底（条目已设）或虚线灰字（快照建议）；null = 两样都没有，不摆。 */
  context: { text: string; ghost: boolean; title: string } | null
  image: boolean
  price: { text: string; title: string } | null
  /** null = 未定价——照实写，不显 $0（NULL ≠ 0，口径层两态）。 */
  unpriced: boolean
}

export function catalogPills(m: CatalogModel): CatalogPills {
  const four: FourPrices = {
    input: m.price_input,
    output: m.price_output,
    cache_read: m.price_cache_read,
    cache_write: m.price_cache_write,
  }
  const unpriced = isUnpriced(four)
  let context: CatalogPills['context'] = null
  if (m.max_input_tokens > 0) {
    context = {
      text: `输入 ≤${fmtTokensShort(m.max_input_tokens)}`,
      ghost: false,
      title: `输入上限（估算）：入站请求体字节数 ÷ 4，条目上设的 ${fmtInt(m.max_input_tokens)}`,
    }
  } else if (m.snapshot_context > 0) {
    context = {
      text: `≈${fmtTokensShort(m.snapshot_context)} · models.dev`,
      ghost: true,
      title: `models.dev 快照说这个模型输入上限约 ${fmtInt(m.snapshot_context)}——条目上没设，只是提示`,
    }
  }
  const tierSuffix =
    m.price_tier_above !== null && m.price_tier_above !== undefined
      ? ` · >${fmtTokensShort(m.price_tier_above)} ↑`
      : ''
  const tierTitle =
    m.price_tier_above !== null && m.price_tier_above !== undefined
      ? `；${fmtTierTitle({
          input: m.price_input,
          output: m.price_output,
          cache_read: m.price_cache_read,
          cache_write: m.price_cache_write,
          cache_write_1h: m.price_cache_write_1h,
          tier_above: m.price_tier_above,
          tier_input: m.price_tier_input,
          tier_output: m.price_tier_output,
          tier_cache_read: m.price_tier_cache_read,
          tier_cache_write: m.price_tier_cache_write,
          tier_cache_write_1h: m.price_tier_cache_write_1h,
        })}`
      : ''
  return {
    protocols: m.protocols.map((p) => ({ label: PROTO_SHORT[p] ?? p, title: p })),
    context,
    image: m.image,
    price: unpriced
      ? null
      : {
          text: `${fmtPrice(m.price_input)}/${fmtPrice(m.price_output)}${tierSuffix}`,
          title: fmtFourTitle(four) + tierTitle,
        },
    unpriced,
  }
}

/**
 * `rows` 由调用方拉好（管理侧 GET /model-catalog、我的侧 GET /my/models——同一形状，
 * 管理侧多来源两格）。管理侧 `onPick` 有值：点行把模型带进上面的接入指引。
 */
export function ModelCatalog({
  rows,
  space,
  onPick,
}: {
  rows: CatalogModel[]
  space: 'admin' | 'my'
  onPick?: (id: string) => void
}) {
  // 常驻搜索（名 / 渠道）：我的侧没有来源列，渠道这半边自然搜不到。
  const [query, setQuery] = useState('')
  const q = query.trim().toLowerCase()
  const list = q
    ? rows.filter((m) => (m.id + ' ' + (m.source ?? '')).toLowerCase().includes(q))
    : rows

  return (
    <section className="section">
      <header className="section-head">
        <h2>模型目录</h2>
        <input
          className="master-search"
          placeholder={space === 'admin' ? '搜模型名或渠道…' : '搜模型名…'}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </header>
      {list.length === 0 ? (
        <Empty>{rows.length === 0 ? '还没有可路由的模型。' : '没有匹配的模型。'}</Empty>
      ) : (
        <div className="catalog-list">
          {list.map((m) => (
            <CatalogRow key={m.id} m={m} space={space} onPick={onPick} />
          ))}
        </div>
      )}
    </section>
  )
}

function CatalogRow({
  m,
  space,
  onPick,
}: {
  m: CatalogModel
  space: 'admin' | 'my'
  onPick?: (id: string) => void
}) {
  const pills = catalogPills(m)
  return (
    <div
      className={'catalog-row' + (onPick ? ' is-pickable' : '')}
      onClick={onPick ? () => onPick(m.id) : undefined}
    >
      <ModelIcon model={m.id} size={24} />
      <div className="catalog-name">
        <div>
          <code>{m.id}</code>
          <div className="catalog-sub">{m.direct ? '直连（渠道/模型）' : '接入点'}</div>
        </div>
        <CopyIconButton value={m.id} title="复制模型名" />
      </div>
      {space === 'admin' && m.source_id !== undefined && (
        <Link to={`/channels/${m.source_id}`} onClick={(e) => e.stopPropagation()}>
          {m.source}
        </Link>
      )}
      <div className="catalog-pills">
        {pills.protocols.map((p) => (
          <span key={p.label} className="cat-pill" title={p.title}>
            {p.label}
          </span>
        ))}
        {pills.context && (
          <span className={'cat-pill' + (pills.context.ghost ? ' is-ghost' : '')} title={pills.context.title}>
            {pills.context.text}
          </span>
        )}
        {pills.image && (
          <span className="cat-pill" title="models.dev 快照说这个模型支持图片输入">
            <ImageIcon />
          </span>
        )}
        {pills.price ? (
          <span className="cat-pill" title={pills.price.title}>
            {pills.price.text}
          </span>
        ) : (
          <span className="cat-pill is-ghost" title="四价全 null = 没记价，不是免费">
            未定价
          </span>
        )}
      </div>
    </div>
  )
}
