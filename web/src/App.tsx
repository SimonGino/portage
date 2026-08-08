import { useCallback, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { api, setUnauthorizedHandler } from './api'
import type { SessionState } from './api'
import Login from './pages/Login'
import Channels from './pages/Channels'
import AccessPoints from './pages/AccessPoints'
import Keys from './pages/Keys'
import Usage from './pages/Usage'
import ChangePassword from './pages/ChangePassword'

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
    <div className="app">
      <nav className="nav">
        <span className="brand">ai-gateway</span>
        <NavLink to="/channels">渠道</NavLink>
        <NavLink to="/access-points">接入点</NavLink>
        <NavLink to="/keys">网关 key</NavLink>
        <NavLink to="/usage">用量</NavLink>
        <span className="spacer" />
        <button className="btn btn-quiet" onClick={() => setPwOpen(true)}>
          改密码
        </button>
        <button
          className="btn btn-quiet"
          onClick={async () => {
            await api.post('/logout')
            await refresh()
          }}
        >
          退出
        </button>
      </nav>

      <main className="main">
        <Routes>
          <Route path="/channels" element={<Channels />} />
          <Route path="/access-points" element={<AccessPoints />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/usage" element={<Usage />} />
          {/* 兜住 /admin 本身以及任何不认识的深链接。用 replace 是为了不在
              浏览器历史里留下一个「回退就又跳一次」的空档。 */}
          <Route path="*" element={<Navigate to="/channels" replace />} />
        </Routes>
      </main>

      {pwOpen && <ChangePassword onClose={() => setPwOpen(false)} onChanged={refresh} />}
    </div>
  )
}
