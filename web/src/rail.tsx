import { createContext, useContext, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

const RailMidEl = createContext<HTMLElement | null>(null)
const RailMidSet = createContext<((el: HTMLElement | null) => void) | null>(null)

/** 包住整页壳：左栏中段的挂点与各页的 RailMid 共用这一份 DOM。 */
export function RailProvider({ children }: { children: ReactNode }) {
  const [el, setEl] = useState<HTMLElement | null>(null)
  return (
    <RailMidSet.Provider value={setEl}>
      <RailMidEl.Provider value={el}>{children}</RailMidEl.Provider>
    </RailMidSet.Provider>
  )
}

/** 放在 App 的 aside 里。只有模型页往这里灌渠道列表。 */
export function RailMidTarget() {
  const setEl = useContext(RailMidSet)
  return <div className="rail-mid" ref={setEl} />
}

/** 把 children portal 进左栏中段。不在模型页时不渲染，中段就空着。 */
export function RailMid({ children }: { children: ReactNode }) {
  const el = useContext(RailMidEl)
  if (!el) return null
  return createPortal(children, el)
}
