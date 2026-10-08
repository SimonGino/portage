// PROTOTYPE #179：视觉方向两版皮的切换条。`?variant=A|B` 落到 <html data-proto>，
// 样式全在 skin.css；DEV 构建才渲染，生产 build 里是空组件。拍板后整段摘掉。
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import './skin.css'

const VARIANTS = [
  ['A', 'magpie 皮（冷灰 · 彩色图标 · 无衬线）'],
  ['B', '暖色混合（羊皮纸 · 衬线 · 只借表面）'],
] as const

export function ProtoSkinBar() {
  const [params, setParams] = useSearchParams()
  const [dark, setDark] = useState(false)
  const key = params.get('variant')
  const idx = Math.max(0, VARIANTS.findIndex(([k]) => k === key))
  const on = key !== null

  useEffect(() => {
    const el = document.documentElement
    if (on) el.dataset.proto = VARIANTS[idx][0]
    else delete el.dataset.proto
    if (on && VARIANTS[idx][0] === 'A' && dark) el.dataset.dark = '1'
    else delete el.dataset.dark
  }, [on, idx, dark])

  const go = (d: number) => {
    const next = VARIANTS[(idx + d + VARIANTS.length) % VARIANTS.length][0]
    params.set('variant', next)
    setParams(params, { replace: true })
  }
  const off = () => {
    params.delete('variant')
    setParams(params, { replace: true })
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
      if (e.key === 'ArrowLeft') go(-1)
      if (e.key === 'ArrowRight') go(1)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  })

  if (!import.meta.env.DEV) return null
  if (!on) {
    return (
      <div className="proto-bar">
        <span className="lbl">#179 现状</span>
        <button type="button" onClick={() => go(0)}>
          看两版皮 →
        </button>
      </div>
    )
  }
  const [k, name] = VARIANTS[idx]
  return (
    <div className="proto-bar">
      <button type="button" onClick={() => go(-1)} aria-label="上一版">
        ←
      </button>
      <span className="lbl">
        {k} · {name}
      </span>
      <button type="button" onClick={() => go(1)} aria-label="下一版">
        →
      </button>
      {k === 'A' && (
        <button type="button" onClick={() => setDark((d) => !d)}>
          {dark ? '亮色' : '暗色'}
        </button>
      )}
      <button type="button" onClick={off}>
        现状
      </button>
    </div>
  )
}
