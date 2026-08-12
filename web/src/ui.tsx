// 几个页面都要用的小零件。刻意不引 UI 框架：整个管理端就四页表格加几个表单，
// 引一套组件库带来的体积和版本负担远超它省下的代码。

import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'

/**
 * useList 把「拉一张列表」这件事收成一个 hook：加载中、出错、重拉。
 *
 * 每次写操作之后都调 reload 重新拉全量，而不是在前端就地改那一行。列表不大，
 * 而后端的写是带校验的事务——就地改意味着前端要自己模拟一遍后端的规则，
 * 迟早对不上（比如删渠道会级联删掉它的候选，页面上那个接入点也变了）。
 *
 * deps 是「查询参数变了要重拉」用的（比如用量页的天数）。fetcher 本身**不进**
 * 依赖数组：调用方基本都是写内联箭头函数，每次渲染都是新引用，进去就是无限重拉。
 */
export function useList<T>(fetcher: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  const ref = useRef(fetcher)
  ref.current = fetcher

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      setData(await ref.current())
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, ...deps])

  return { data, error, loading, reload, setError }
}

export function ErrorBar({ message }: { message?: string }) {
  if (!message) return null
  return <div className="bar bar-error">{message}</div>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>
}

export function Card({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      <header className="card-head">
        <h2>{title}</h2>
        {action}
      </header>
      {children}
    </section>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  )
}

/** Dialog 是个受控的模态框。表单都塞在里面，列表页只留表格。 */
export function Dialog({
  title,
  onClose,
  wide,
  guard,
  children,
}: {
  title: string
  onClose: () => void
  /** wide：内容是列表而不是表单时放宽（模型挑选面板）。表单不该跟着变宽——
   *  一行输入框拉到 900px 只会让标签和输入内容离得老远。 */
  wide?: boolean
  /** guard：框里攒着还没提交的东西（比如勾了一半的模型），点遮罩不关。
   *  Esc 不受影响——那是明确的按键，而遮罩点击多半是想点框边上的东西点歪了。 */
  guard?: boolean
  children: ReactNode
}) {
  // Esc 关闭。表单填错了想退出去，第一反应是按 Esc 而不是找那个叉。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="overlay" onMouseDown={guard ? undefined : onClose}>
      {/* 阻止冒泡：在框里面按下鼠标不该关掉框（选文本时很容易拖到框外） */}
      <div
        className={'dialog' + (wide ? ' dialog-wide' : '')}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="dialog-head">
          <h3>{title}</h3>
          <button className="icon" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </header>
        {children}
      </div>
    </div>
  )
}

/**
 * Confirm 是删除用的两段式按钮：点一下变成「确定删除？」，再点才真删。
 *
 * 不用 window.confirm——它是浏览器模态框，会把整个页面的事件循环挡住，
 * 而这个项目的调试指引明确要避开原生对话框。
 */
export function Confirm({
  label = '删除',
  ghost,
  onConfirm,
}: {
  label?: string
  /** ghost：未举起时不画边框，悬停才显形。列表里每行都挂一个删除按钮时用它——
   *  一排同等分量的描边按钮会盖过行首的正主。举起后仍是实心红，不受影响。 */
  ghost?: boolean
  onConfirm: () => void
}) {
  const [armed, setArmed] = useState(false)

  useEffect(() => {
    if (!armed) return
    // 举起来但没按第二下的，3 秒自动放下：一个红着的「确定删除？」按钮
    // 一直挂在那儿，下次误点就真删了。
    const t = setTimeout(() => setArmed(false), 3000)
    return () => clearTimeout(t)
  }, [armed])

  if (!armed) {
    return (
      <button className={'btn ' + (ghost ? 'btn-ghost' : 'btn-quiet')} onClick={() => setArmed(true)}>
        {label}
      </button>
    )
  }
  return (
    <button
      className="btn btn-danger"
      onClick={() => {
        setArmed(false)
        onConfirm()
      }}
    >
      确定{label}？
    </button>
  )
}

/**
 * CopyCode 显示一段要被原样抄进客户端配置的字符串，点一下复制。
 *
 * 限定名（`渠道名/纳管模型名`）用得最多——它是客户端 `model` 字段要填的东西，手抄
 * 一个带斜杠的长串很容易漏字符，而漏了的表现是 404。
 */
// label 让「显示的」与「复制的」分开：渠道页的网格单元里横向空间有限，摆的是裸模型名，
// 但客户端 `model` 字段要填的是限定名（口径层 v0.32），复制走的是后者。
export function CopyCode({
  value,
  title,
  label,
  className,
}: {
  value: string
  title?: string
  label?: string
  className?: string
}) {
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(t)
  }, [copied])

  return (
    <button
      type="button"
      className={'copycode' + (copied ? ' is-copied' : '') + (className ? ' ' + className : '')}
      title={title ?? '点击复制'}
      onClick={async () => {
        // clipboard API 在非 HTTPS 的非 localhost 页面上不可用（局域网访问就是这种
        // 情况）。失败不报错，那段文字本来就是选中就能复制的。
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
        } catch {
          setCopied(false)
        }
      }}
    >
      <code>{label ?? value}</code>
      <span className="copycode-mark" aria-hidden>
        {copied ? '已复制' : '复制'}
      </span>
    </button>
  )
}

export function Toggle({ on, onChange }: { on: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} />
      <span />
    </label>
  )
}

/** fmtInt 给用量表用：四位以上加千分位，看 token 数时一眼能分出量级。 */
export function fmtInt(n: number | null | undefined) {
  if (n === null || n === undefined) return '—'
  return n.toLocaleString('en-US')
}

/**
 * fmtTime 把后端的 SQLite 时间戳（UTC，形如 `2026-08-07 03:12:44`）显示成本地时间。
 *
 * 不用 `toLocaleString()` 的默认形态：它在中文环境下吐出 `2026/8/9 13:50:30`、
 * 英文环境下是 `8/9/2026, 1:50:30 PM`，前者月日不补零、后者带 AM/PM——两种都
 * 不等宽，一列时间戳看上去参差不齐，而这一列的用处正是竖着扫。年份省掉：
 * 流水表只回最近一百条，年份是恒定噪音。
 */
export function fmtTime(s: string) {
  if (!s) return '—'
  // SQLite 的 CURRENT_TIMESTAMP 不带时区后缀，直接 new Date() 在浏览器里会被
  // 当成本地时间，于是显示出来比真实时间早了 8 小时。补个 Z 说明它是 UTC。
  const iso = s.includes('T') ? s : s.replace(' ', 'T') + 'Z'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return s
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
