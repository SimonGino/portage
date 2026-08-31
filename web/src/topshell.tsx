import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { User } from './api'
import { PortageMark } from './brand'

/**
 * 统一顶栏壳（DESIGN §2，v0.54）：管理与「我的」两空间共用这一只壳——品牌 +
 * 一排 tab + 右侧动作区，主区是居中的画布列。此前管理空间是 248px 左栏
 * （v0.23~v0.53），PO 2026-08-31 裁定两空间统一成顶栏，左栏退役。
 *
 * 画布列三档宽：narrow 760（「我的」单列文档流）、默认 920（管理端表格页）、
 * wide 1320（流水表与主从两栏这种横向吃宽的页）。
 */
export function TopShell({
  tabs,
  right,
  width,
  children,
}: {
  tabs: ReactNode
  right: ReactNode
  width?: 'narrow' | 'wide'
  children: ReactNode
}) {
  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">
          <PortageMark size={20} />
          <b>Portage</b>
        </span>
        <nav className="topnav" aria-label="主导航">
          {tabs}
        </nav>
        <div className="topbar-right">{right}</div>
      </header>
      <main className={'sheet' + (width ? ` sheet-${width}` : '')}>{children}</main>
    </div>
  )
}

/**
 * 顶栏右侧的头像菜单：账号身份 + 一列动作。管理空间的导出/导入/改密码/退出
 * 从左栏底部搬进来（PO 2026-08-31 裁定），「我的」空间只有退出——密码在账号页。
 *
 * children 是渲染函数：动作项自己决定点完关不关菜单（改密码/退出关，导出/导入
 * 不关——它们的弹框就长在菜单的 DOM 里，菜单一关弹框跟着卸载，流程断在半路）。
 */
export function AvatarMenu({
  user,
  children,
}: {
  user?: User
  children: (close: () => void) => ReactNode
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    // 弹框开着时（导出失败/导入试算的 overlay 是菜单的 DOM 子孙）外点与 Escape
    // 都不关菜单：overlay 盖满全屏，mousedown 落在它上面 contains 本来就为真；
    // Escape 交给 Dialog 自己关，菜单同帧跟着关会把「关弹框」误伤成「全关」。
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !ref.current?.querySelector('.overlay')) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div className="avatar-menu" ref={ref}>
      <button
        type="button"
        className="acct-avatar"
        aria-haspopup="menu"
        aria-expanded={open}
        title={user?.email}
        onClick={() => setOpen((o) => !o)}
      >
        {(user?.display_name || user?.email || '管')[0].toUpperCase()}
      </button>
      {open && (
        <div className="avatar-pop" role="menu">
          <div className="who">
            <strong>{user?.display_name || '管理员'}</strong>
            <span>{user?.email ?? '本机唯一管理员'}</span>
          </div>
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  )
}
