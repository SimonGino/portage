import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api, setUnauthorizedHandler } from './api'
import { PortageMark } from './brand'
import type { SessionState } from './api'
import Login from './pages/Login'
import Channels from './pages/Channels'
import AccessPoints from './pages/AccessPoints'
import Keys from './pages/Keys'
import Logs from './pages/Logs'
import Rankings from './pages/Rankings'
import ChangePassword from './pages/ChangePassword'
import { RailMidTarget, RailProvider } from './rail'

const NAV: { to: string; label: string; icon: ReactNode }[] = [
  {
    to: '/channels',
    label: '模型',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <path d="M4 8h16M7 12h10M10 16h4" />
      </svg>
    ),
  },
  {
    to: '/keys',
    label: 'API Key',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <circle cx="8" cy="12" r="3" />
        <path d="M11 12h9l-2 2m2-2-2-2" />
      </svg>
    ),
  },
  {
    to: '/logs',
    label: '调用记录',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <path d="M8 7h11M8 12h11M8 17h8" />
        <path d="M5 7.2v.01M5 12.2v.01M5 17.2v.01" />
      </svg>
    ),
  },
  {
    to: '/access-points',
    label: '接入点',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <circle cx="8" cy="12" r="3" />
        <circle cx="16" cy="12" r="3" />
        <path d="M11 12h2" />
      </svg>
    ),
  },
  {
    to: '/rankings',
    label: '排行',
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <path d="M6 17V11M12 17V7M18 17v-4" />
      </svg>
    ),
  },
]

export default function App() {
  const [session, setSession] = useState<SessionState | null>(null)
  const [pwOpen, setPwOpen] = useState(false)

  const refresh = useCallback(async () => {
    try {
      setSession(await api.get<SessionState>('/session'))
    } catch {
      // /session 本身挂了（网关没起、代理没通）也当成未登录，让人看到登录页而不是白屏。
      setSession({ authenticated: false, password_set: true })
    }
  }, [])

  useEffect(() => {
    void refresh()
    // 任何一个接口回 401（会话过期、或者别处改了密码把会话全吊销了），
    // 立刻把界面切回登录页，而不是让人对着一个不断报错的表格发呆。
    setUnauthorizedHandler(() => setSession({ authenticated: false, password_set: true }))
  }, [refresh])

  if (session === null) return <div className="boot">加载中…</div>

  if (!session.authenticated) {
    return <Login passwordSet={session.password_set} onLoggedIn={refresh} />
  }

  return (
    <RailProvider>
      <Shell onPassword={() => setPwOpen(true)} onLogout={refresh} />
      {pwOpen && <ChangePassword onClose={() => setPwOpen(false)} onChanged={refresh} />}
    </RailProvider>
  )
}

function Shell({ onPassword, onLogout }: { onPassword: () => void; onLogout: () => void }) {
  const loc = useLocation()
  const wide = loc.pathname.startsWith('/logs')

  return (
    <div className="app">
      <aside className="rail" aria-label="导航">
        <span className="brand">
          <PortageMark size={22} />
          <b>Portage</b>
        </span>
        <nav className="nav" aria-label="五项">
          {NAV.map((item) => (
            <NavLink key={item.to} to={item.to}>
              {item.icon}
              {item.label}
            </NavLink>
          ))}
        </nav>
        <RailMidTarget />
        <div className="account">
          <div className="who">
            <div className="acct-avatar" aria-hidden>
              管
            </div>
            <div>
              <strong>管理员</strong>
              <span>本机唯一管理员</span>
            </div>
          </div>
          <div className="acct-acts">
            <button type="button" onClick={onPassword}>
              改密码
            </button>
            <button
              type="button"
              onClick={async () => {
                await api.post('/logout')
                await onLogout()
              }}
            >
              退出
            </button>
          </div>
        </div>
      </aside>

      <main className={'main' + (wide ? ' is-wide' : '')}>
        <Routes>
          <Route path="/channels" element={<Channels />} />
          {/* 选中的渠道进 URL（口径层 v0.45 主从两栏）：刷新、回退都还留在同一个
              渠道上。`new` 占的是同一段位置——新建时右栏就是那张空表单。 */}
          <Route path="/channels/:id" element={<Channels />} />
          <Route path="/access-points" element={<AccessPoints />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/rankings" element={<Rankings />} />
          {/* 概览已从导航拿掉（口径层 v0.75）。老地址与拆页前的 /usage 都落到排行，
              不交给下面那个 `*`：开着旧标签刷新会掉到渠道页上，那不像跳转，像页面没了。 */}
          <Route path="/overview" element={<Navigate to="/rankings" replace />} />
          <Route path="/usage" element={<Navigate to="/rankings" replace />} />
          {/* 兜住 /admin 本身以及任何不认识的深链接。用 replace 是为了不在
              浏览器历史里留下一个「回退就又跳一次」的空档。 */}
          <Route path="*" element={<Navigate to="/channels" replace />} />
        </Routes>
      </main>
    </div>
  )
}
