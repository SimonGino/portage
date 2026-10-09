import { useEffect, useState } from 'react'
import { api } from '../../api'
import type { ChannelPreset } from '../../api'
import { ErrorBar } from '../../ui'
import { Avatar } from '../../icons'
import { ChannelForm } from './form'
import { filterPresets } from './derive'

// 目录三组（DESIGN v0.72；v0.77 加第三组「订阅」）：组内顺序即清单顺序；没有条目的组
// 整组不渲染（订阅组首版只有 ChatGPT 一块，Copilot 等实现跑通再上、不做占位）。
const GROUPS: { id: ChannelPreset['group']; label: string }[] = [
  { id: 'vendor', label: '厂商 API' },
  { id: 'relay', label: '中转' },
  { id: 'subscription', label: '订阅' },
]

/**
 * NewChannel 是新建渠道的两态（DESIGN v0.72，口径层 v1.47）：先是预设目录，选中
 * tile（或「自定义」）后主区切成表单，表单顶部「换一个」回目录。主区整页切换，
 * 不开弹框。零渠道时渠道页空态就是它（不给 onCancel——没有别处可回）。
 */
export function NewChannel({
  onCancel,
  onSaved,
}: {
  onCancel?: () => void
  onSaved: (id: number) => void
}) {
  // undefined = 还在目录；null = 自定义（原空白表单）；否则是选中的预设。
  const [chosen, setChosen] = useState<ChannelPreset | null | undefined>(undefined)
  if (chosen === undefined) return <PresetCatalog onPick={setChosen} onCancel={onCancel} />
  return (
    <ChannelForm
      key={chosen?.id ?? 'custom'}
      channel={null}
      preset={chosen}
      onRepick={() => setChosen(undefined)}
      onCancel={onCancel}
      onSaved={onSaved}
    />
  )
}

/**
 * PresetCatalog 是目录态：搜索 + 「厂商 API」「中转」「订阅」三组 tile + 底部「自定义」行。
 * 清单拉失败只挂错误条，自定义照常可走——目录是捷径，不是建渠道的前提。
 */
function PresetCatalog({
  onPick,
  onCancel,
}: {
  onPick: (p: ChannelPreset | null) => void
  onCancel?: () => void
}) {
  const [list, setList] = useState<ChannelPreset[] | null>(null)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')

  useEffect(() => {
    let gone = false
    api
      .get<ChannelPreset[]>('/channel-presets')
      .then((r) => {
        if (!gone) setList(r ?? [])
      })
      .catch((e) => {
        if (gone) return
        setList([])
        setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      gone = true
    }
  }, [])

  const visible = filterPresets(list ?? [], query)

  return (
    <section className="section preset-cat">
      <header className="section-head">
        <h2>选一家接入</h2>
        {onCancel && (
          <div className="row-actions">
            <button type="button" className="btn btn-quiet" onClick={onCancel}>
              取消
            </button>
          </div>
        )}
      </header>
      <input
        autoFocus
        className="master-search preset-search"
        placeholder="搜名称、id 或域名…"
        aria-label="搜预设"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />
      <ErrorBar message={error} />
      {list === null && <div className="muted">加载中…</div>}
      {list !== null && query.trim() !== '' && visible.length === 0 && (
        <p className="muted preset-miss">没有叫「{query.trim()}」的，走下面的自定义。</p>
      )}
      {GROUPS.map((g) => {
        const items = visible.filter((p) => p.group === g.id)
        if (items.length === 0) return null
        return (
          <div key={g.id} className="preset-group">
            <div className="preset-group-label">{g.label}</div>
            <div className="preset-grid">
              {items.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  className="preset-tile"
                  title={p.note ? `${p.name} · ${p.note}` : p.name}
                  onClick={() => onPick(p)}
                >
                  <Avatar vendor={p.icon} fallback={p.name} size={20} />
                  <span className="preset-tile-name">{p.name}</span>
                  {p.plans && <span className="preset-tile-plans">{p.plans.length} 套</span>}
                </button>
              ))}
            </div>
          </div>
        )
      })}
      <button type="button" className="preset-custom" onClick={() => onPick(null)}>
        <strong>自定义</strong>
        <span className="muted">任何 OpenAI / Anthropic 兼容地址</span>
      </button>
    </section>
  )
}
