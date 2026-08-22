// 挑一个值用的两个控件：选项少用 Segmented，选项多用 Picker。
//
// 原生 `<select>` 退场的原因不是它不好看，是它装不下需要装的东西：候选那一格要按渠道
// 分组、每行带供应商图标、还要能搜——`<option>` 里塞不进元素，`<optgroup>` 也不接受
// 自定义渲染。协议那一格反过来，三个值全摆出来比藏在下拉里少一次点击。

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'

export interface Option<T> {
  value: T
  label: string
  /** hint 显示在标签右侧的次要文字（base_url、协议名这类）。 */
  hint?: string
  /** title 只进 tooltip，不占版面——横排的按钮里塞不下一条子路径。 */
  title?: string
  icon?: ReactNode
  /** group 相同的选项归在一个小标题下，按首次出现的顺序排。 */
  group?: string
  /** keywords 参与搜索但不显示——渠道的 base_url 就靠它搜得到。 */
  keywords?: string
}

/**
 * Segmented 是选项固定且少（2~4 个）时的选择器：全部摆出来，点哪个是哪个。
 */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T
  options: Option<T>[]
  onChange: (v: T) => void
}) {
  return (
    <div className="segmented" role="radiogroup">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          className={'segment' + (o.value === value ? ' is-on' : '')}
          onClick={() => onChange(o.value)}
        >
          {o.icon}
          <span>{o.label}</span>
        </button>
      ))}
    </div>
  )
}

/**
 * Picker 是选项多、需要分组和搜索时的选择器。
 *
 * 展开时才渲染列表：渠道页可能有几十个纳管模型，全部常驻 DOM 只是给每次重渲染加负担。
 * 搜索框在选项超过 `searchAfter` 个时才出现——七八个选项配一个搜索框是噪音。
 */
export function Picker<T extends string | number>({
  value,
  options,
  onChange,
  placeholder = '请选择…',
  searchAfter = 8,
  disabled,
}: {
  value: T | null
  options: Option<T>[]
  onChange: (v: T) => void
  placeholder?: string
  searchAfter?: number
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  // active 是键盘高亮的那一项在**过滤后**列表里的下标。
  const [active, setActive] = useState(0)
  const rootRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const selected = options.find((o) => o.value === value) ?? null
  const searchable = options.length > searchAfter

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return options
    return options.filter((o) =>
      (o.label + ' ' + (o.group ?? '') + ' ' + (o.hint ?? '') + ' ' + (o.keywords ?? ''))
        .toLowerCase()
        .includes(q),
    )
  }, [options, query])

  // 关掉时把搜索词一起清掉：留着的话下次展开看到的是一份被上次搜索裁过的列表，
  // 而那个搜索框可能压根没显示出来（选项数正好在 searchAfter 边界上）。
  useEffect(() => {
    if (open) return
    setQuery('')
    setActive(0)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  // 展开就把焦点送进搜索框，不用再点一下。没有搜索框时焦点留在触发按钮上，
  // 上下键照样能用（keydown 挂在容器上）。
  useLayoutEffect(() => {
    if (open && searchable) searchRef.current?.focus()
  }, [open, searchable])

  // 键盘移动高亮时把它滚进视野。用 block:'nearest' 而不是 scrollIntoView 默认值：
  // 默认值会把列表整个居中，一路按下去时页面在抖。
  useLayoutEffect(() => {
    if (!open) return
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [open, active])

  function commit(o: Option<T>) {
    onChange(o.value)
    setOpen(false)
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      if (open) {
        // 在展开的下拉里按 Esc 只该收起下拉，不该顺带把外面那个 Dialog 也关了
        // ——那会把填了一半的表单丢掉。
        e.stopPropagation()
        setOpen(false)
      }
      return
    }
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
        e.preventDefault()
        setOpen(true)
      }
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (filtered.length === 0) return
      const step = e.key === 'ArrowDown' ? 1 : -1
      setActive((i) => (i + step + filtered.length) % filtered.length)
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      const o = filtered[active]
      if (o) commit(o)
    }
  }

  // 分组保持选项数组里首次出现的顺序，不排序：调用方按「渠道建立顺序」传进来的，
  // 重排一遍只会让人对不上渠道页看到的次序。
  const groups: { name: string | null; items: Option<T>[] }[] = []
  for (const o of filtered) {
    const name = o.group ?? null
    const last = groups[groups.length - 1]
    if (last && last.name === name) last.items.push(o)
    else groups.push({ name, items: [o] })
  }

  let index = -1
  return (
    <div className={'picker' + (open ? ' is-open' : '')} ref={rootRef} onKeyDown={onKeyDown}>
      <button
        type="button"
        className="picker-trigger"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {selected ? (
          <span className="picker-value">
            {selected.icon}
            <span className="picker-label">{selected.label}</span>
            {selected.hint && <span className="picker-hint">{selected.hint}</span>}
          </span>
        ) : (
          <span className="picker-placeholder">{placeholder}</span>
        )}
        <span className="picker-caret" aria-hidden>
          ▾
        </span>
      </button>

      {open && (
        <div className="picker-pop">
          {searchable && (
            <input
              ref={searchRef}
              className="picker-search"
              placeholder="搜索…"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setActive(0)
              }}
            />
          )}
          <div className="picker-list" role="listbox" ref={listRef}>
            {filtered.length === 0 && <div className="picker-empty">没有匹配的选项</div>}
            {groups.map((g, gi) => (
              <div key={g.name ?? gi} className="picker-group">
                {g.name && <div className="picker-group-name">{g.name}</div>}
                {g.items.map((o) => {
                  index++
                  const i = index
                  return (
                    <button
                      key={String(o.value)}
                      type="button"
                      role="option"
                      aria-selected={o.value === value}
                      data-active={i === active}
                      className={
                        'picker-option' +
                        (o.value === value ? ' is-selected' : '') +
                        (i === active ? ' is-active' : '')
                      }
                      // 用 mousemove 而不是 mouseenter：列表被键盘滚动时，光标下方
                      // 换了一行会触发 mouseenter，高亮就被鼠标抢回去了。
                      onMouseMove={() => setActive(i)}
                      onClick={() => commit(o)}
                    >
                      {o.icon}
                      <span className="picker-label">{o.label}</span>
                      {o.hint && <span className="picker-hint">{o.hint}</span>}
                    </button>
                  )
                })}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

/** Chips 把一串字符串显示成可删的标签，配一个输入框往里加。key 白名单用它。 */
export function Chips({
  items,
  onChange,
  placeholder,
  suggestions = [],
  renderIcon,
}: {
  items: string[]
  onChange: (next: string[]) => void
  placeholder?: string
  suggestions?: Option<string>[]
  renderIcon?: (item: string) => ReactNode
}) {
  const [draft, setDraft] = useState('')
  const unused = suggestions.filter((s) => !items.includes(s.value))

  function add(v: string) {
    const t = v.trim()
    if (!t || items.includes(t)) {
      setDraft('')
      return
    }
    onChange([...items, t])
    setDraft('')
  }

  return (
    <div className="chips">
      <div className="chips-row">
        {items.map((it) => (
          <span key={it} className="chip">
            {renderIcon?.(it)}
            <code>{it}</code>
            <button
              type="button"
              className="chip-x"
              aria-label={`移除 ${it}`}
              onClick={() => onChange(items.filter((x) => x !== it))}
            >
              ×
            </button>
          </span>
        ))}
        <input
          className="chips-input"
          placeholder={items.length === 0 ? placeholder : ''}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ',') {
              e.preventDefault()
              add(draft)
              return
            }
            // 输入框空着时退格删掉最后一个标签，跟收件人框一个手感。
            if (e.key === 'Backspace' && draft === '' && items.length > 0) {
              onChange(items.slice(0, -1))
            }
          }}
          onBlur={() => add(draft)}
        />
      </div>
      {unused.length > 0 && (
        <div className="chips-suggest">
          {unused.map((s) => (
            <button key={s.value} type="button" className="chip chip-add" onClick={() => add(s.value)}>
              {s.icon}
              <code>{s.label}</code>
              <span className="chip-plus">+</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
