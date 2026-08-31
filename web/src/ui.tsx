// 几个页面都要用的小零件。刻意不引 UI 框架：整个管理端就四页表格加几个表单，
// 引一套组件库带来的体积和版本负担远超它省下的代码。

import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { IconCheck, IconCopy, IconEye, IconEyeOff } from './icons/acts'

/**
 * useList 把「拉一张列表」这件事收成一个 hook：加载中、出错、重拉。
 *
 * 每次写操作之后都调 reload 重新拉全量，而不是在前端就地改那一行。列表不大，
 * 而后端的写是带校验的事务——就地改意味着前端要自己模拟一遍后端的规则，
 * 迟早对不上（比如删渠道会级联删掉它的候选，页面上那个接入点也变了）。
 *
 * deps 是「查询参数变了要重拉」用的（比如概览页的天数）。fetcher 本身**不进**
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

/**
 * Card 现在不是卡片，是一个区段——名字留着只为省一次全站改名。
 *
 * 去掉边框/底色/阴影是执行 DESIGN.md §3「页面是连续画布：区块靠间距分隔，不逐区包
 * 卡片、不嵌套面板」。原先每页一张卡、渠道页还在卡里套渠道行、行里再套模型格，
 * 三层框叠出来的层级其实一层都不承载信息——标题的字号字重加区段间距已经说清了
 * 「这是一块」。§9 自查第 5 条（删掉任意一个边框，层级是否依然成立）在这一处成立。
 */
export function Card({
  title,
  action,
  children,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="section">
      <header className="page-head">
        <h1>{title}</h1>
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
        {/* 正文区单独滚：框整体限高（84vh），内容再长头部也钉在原地，
            尾排按钮靠 CSS sticky 钉底——签字键不该被清单顶出屏幕。 */}
        <div className="dialog-body">{children}</div>
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
  confirm,
  ghost,
  danger,
  onConfirm,
}: {
  label?: string
  /** 举起后的确认文案，默认「确定{label}？」。删除会连带别的后果时用它把后果
   *  写进按钮里（如「删除并停用渠道？」）——提示要长在人下一步必按的那个键上。 */
  confirm?: string
  /** ghost：未举起时不画边框，悬停才显形。列表里每行都挂一个删除按钮时用它——
   *  一排同等分量的描边按钮会盖过行首的正主。举起后仍是实心红，不受影响。 */
  ghost?: boolean
  /** danger：未举起就画实心红。留给「按下即重排整份配置」的弹框主操作——全量
   *  覆盖这种动作第一眼就该危险，而不是等到举起那一下才变红。与 ghost 互斥：
   *  同时传时 ghost 优先（两个都是「未举起态」的形状，不叠）。 */
  danger?: boolean
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

  // type="button"：这个组件也会站在表单里（上游设置弹框的 foot），默认 submit
  // 会让「点一下删除」顺手把表单交出去。
  if (!armed) {
    return (
      <button
        type="button"
        className={'btn ' + (ghost ? 'btn-ghost' : danger ? 'btn-danger' : 'btn-quiet')}
        onClick={() => setArmed(true)}
      >
        {label}
      </button>
    )
  }
  return (
    <button
      type="button"
      className="btn btn-danger"
      onClick={() => {
        setArmed(false)
        onConfirm()
      }}
    >
      {confirm ?? `确定${label}？`}
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

/**
 * CopyIconButton 是值旁的复制微钮（.act-icon，DESIGN v0.38 ② 的「值旁动作」档）：
 * 点了把 value 抄进剪贴板，成功原地变 ✓ 绿一拍。复制凭证的 SecretValue、复制
 * 限定名的 CopyCode 各有各的形状，这个只服务「一行值 + 贴身微钮」的行——baseurl
 * 预览地址这类。clipboard API 在非 HTTPS 的非 localhost 页面上不可用（局域网访问
 * 就是这种情况）：失败不报错，值本身就在页面上，选中抄走总比一颗报错钮强。
 */
export function CopyIconButton({ value, title = '复制' }: { value: string; title?: string }) {
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(t)
  }, [copied])

  return (
    <button
      type="button"
      className={'act-icon' + (copied ? ' is-copied' : '')}
      aria-label="复制"
      title={copied ? '已复制' : title}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
        } catch {
          // clipboard 不可用的环境：不报错。传进来的 value 本来就显示在页面上。
        }
      }}
    >
      {copied ? <IconCheck /> : <IconCopy />}
    </button>
  )
}

/** mask 只露头尾：头几位是「这是哪一类 key」（sk-ant / sk-ptg），尾四位是「这是哪一把」。
 *  短到没法既遮住又留出线索时全遮——留两位等于没遮。 */
function mask(v: string) {
  if (v.length <= 12) return '•'.repeat(Math.max(v.length, 8))
  return v.slice(0, 6) + '••••••••' + v.slice(-4)
}

/**
 * SecretValue 是一串密钥在页面上的样子：默认掩码，一颗眼睛切换明文，一颗按钮复制。
 *
 * 口径层 v0.47 之前上游凭证与网关 key 都不回读，页面上只有名字——PO 裁定那样「没法
 * 直观地表达这是哪一把」，于是两处都改成可看可复制。掩码是默认态而不是保护措施：
 * 值就在这一份响应里，遮住只是免得截图和录屏顺手把它带出去。
 *
 * 复制的永远是全串，跟当前是不是明文态无关——「先点显示再手动选中」正是这颗按钮
 * 要省掉的那几步。
 */
export function SecretValue({ value, empty }: { value: string; empty?: ReactNode }) {
  const [shown, setShown] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 1200)
    return () => clearTimeout(t)
  }, [copied])

  // 值拿不到的情形是真实存在的：加 key_plain 那一列之前建的网关 key 只剩哈希，
  // 哈希不可逆。这时候摆一串假掩码是撒谎，得说清楚它为什么不在。
  if (!value) return <span className="muted">{empty ?? '—'}</span>

  return (
    <span className="secret">
      <code className="secret-value">{shown ? value : mask(value)}</code>
      <button
        type="button"
        className="act-icon"
        onClick={() => setShown((v) => !v)}
        aria-label={shown ? '隐藏' : '显示'}
        title={shown ? '藏起来' : '看明文'}
      >
        {shown ? <IconEyeOff /> : <IconEye />}
      </button>
      <button
        type="button"
        className={'act-icon' + (copied ? ' is-copied' : '')}
        aria-label="复制"
        onClick={async () => {
          // clipboard API 在非 HTTPS 的非 localhost 页面上不可用（局域网访问就是
          // 这种情况）。失败了就把明文亮出来，让人自己选中——总比一颗没反应的按钮强。
          try {
            await navigator.clipboard.writeText(value)
            setCopied(true)
          } catch {
            setShown(true)
          }
        }}
        title={copied ? '已复制' : '复制全串'}
      >
        {copied ? <IconCheck /> : <IconCopy />}
      </button>
    </span>
  )
}

/**
 * Toggle 是「启用/停用」的开关，长成一段可点的状态文字而不是 iOS 拨杆。
 *
 * 拨杆有两个毛病，DESIGN.md §4 都点了名：一是它把状态压进一个纯色块，靠位置和颜色
 * 表达，而「启用/停用」是**状态**，规矩是语义色 + 文字标注；二是那抹蓝跟主按钮、
 * 导航选中态用的是同一个 accent，颜色不再承载语义（§1.4），一屏里蓝得到处都是。
 *
 * 现在按下去之前就能读出此刻是什么：`● 启用` / `○ 已停用`。圆点是非颜色线索——
 * §3 要求语义色必须配一个不靠颜色的线索，色盲和灰度截图下都还认得出。
 */
/** StateText 是 Toggle 的只读版：状态一样地写出来，但这里改不了（比如接入点的启用
 *  状态在编辑表单里改）。跟 Toggle 共用同一套类，免得同一件事在两页长得不一样。 */
/**
 * DetailBlock 是模型页身份条下的清单区块（「API 地址」「上游凭证」共用这一个壳）。
 *
 * v0.35 起回到常驻展开（推翻 v0.34 的默认收起）：管理、检测、每协议的预览地址
 * 都得一眼可见、一击可点，折一层就是多一步。v0.94「接一家上游要填的全部，
 * 不该藏两层」照旧成立——也不塞回「上游设置」弹框。action 摆在标题旁，给
 * 「编辑 / 完成」这类切换整块形态的文字动作。
 */
export function DetailBlock({
  title,
  action,
  children,
}: {
  title: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="detail-block">
      <div className="detail-block-head">
        <span className="detail-block-title">{title}</span>
        {/* 动作整组右置（DESIGN v0.45）：包装层让 margin-left:auto 只落一次，成对
            动作（管理/检测）的贴近规则也随之挂在容器上，不再依赖头部层的选择器。 */}
        <div className="detail-block-acts">{action}</div>
      </div>
      {children}
    </section>
  )
}

export function StateText({ on }: { on: boolean }) {
  return (
    <span className={'statetoggle is-static' + (on ? ' is-on' : '')}>
      <span className="statetoggle-dot" aria-hidden>
        {on ? '●' : '○'}
      </span>
      {on ? '启用' : '已停用'}
    </span>
  )
}

export function Toggle({ on, onChange, label }: {
  on: boolean
  onChange: (v: boolean) => void
  /** 被开关的东西叫什么，只进 title：「点击停用 gpt-4o」比光一个「点击停用」有用。 */
  label?: string
}) {
  const what = label ? ` ${label}` : ''
  return (
    <button
      type="button"
      className={'statetoggle' + (on ? ' is-on' : '')}
      title={on ? `点击停用${what}` : `点击启用${what}`}
      onClick={() => onChange(!on)}
    >
      <span className="statetoggle-dot" aria-hidden>
        {on ? '●' : '○'}
      </span>
      {on ? '启用' : '已停用'}
    </button>
  )
}

/** fmtInt 给用量表用：四位以上加千分位，看 token 数时一眼能分出量级。 */
export function fmtInt(n: number | null | undefined) {
  if (n === null || n === undefined) return '—'
  return n.toLocaleString('en-US')
}

/** 大数缩写成 12.3k / 4.5M：指标条与排行上要的是量级，精确值走 title。 */
export function fmtCompact(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k'
  return (n / 1_000_000).toFixed(1) + 'M'
}

/**
 * fmtMoney 是金额的标准档（DESIGN §12）：一律 `$X.XX` 两位小数，不足一分显
 * `<$0.01`——两位小数抹成 `$0.00` 会把「花了一点」说成「没花」。0 就是 $0.00：
 * 真没花与真免费在合计上是同一个数。明细展开处用 fmtMoneyDetail 的四位。
 */
export function fmtMoney(n: number) {
  if (n > 0 && n < 0.01) return '<$0.01'
  return '$' + n.toFixed(2)
}

/** fmtMoneyDetail 是明细档：四位小数，流水详情与单笔账目用。 */
export function fmtMoneyDetail(n: number) {
  return '$' + n.toFixed(4)
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
