import { useCallback, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { LEGACY_REDIRECTS, NAV } from './routes'
import { api, setUnauthorizedHandler } from './api'
import type { SessionState } from './api'
import Login from './pages/Login'
import Channels from './pages/Channels'
import AccessPoints from './pages/AccessPoints'
import Keys from './pages/Keys'
import Logs from './pages/Logs'
import Rankings from './pages/Rankings'
import Pricing from './pages/Pricing'
import Users from './pages/Users'
import ChangePassword from './pages/ChangePassword'
import Register from './pages/auth/Register'
import Forgot from './pages/auth/Forgot'
import Reset from './pages/auth/Reset'
import Verify from './pages/auth/Verify'
import VerifyGate from './pages/auth/VerifyGate'
import OAuthComplete from './pages/auth/OAuthComplete'
import MySpace from './pages/my/MySpace'
import { AvatarMenu, TopShell } from './topshell'
import { IconGear } from './icons/acts'
import { SettingsDialog } from './settings'

export default function App() {
  const [session, setSession] = useState<SessionState | null>(null)
  const [pwOpen, setPwOpen] = useState(false)
  const loc = useLocation()

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

  // 门外的几页（#72）不看会话就能进：验证/重置链接是从邮件里点开的，那个浏览器
  // 多半根本没登录；OAuth 完成注册页则是回调 302 过来的。它们必须先于登录闸渲染。
  const outer = (
    <Routes>
      <Route path="/register" element={<Register onDone={refresh} />} />
      <Route path="/forgot" element={<Forgot />} />
      <Route path="/reset" element={<Reset />} />
      <Route path="/verify" element={<Verify onVerified={refresh} />} />
      <Route path="/oauth-complete" element={<OAuthComplete onDone={refresh} />} />
    </Routes>
  )
  if (['/register', '/forgot', '/reset', '/verify', '/oauth-complete'].includes(loc.pathname)) {
    return outer
  }

  if (session === null) return <div className="boot">加载中…</div>

  if (!session.authenticated) {
    return <Login passwordSet={session.password_set} onLoggedIn={refresh} />
  }

  // 未验证可登录但功能全锁（#62 决议 2）：整个壳都不给，只有去验证页。
  if (session.user && !session.user.email_verified) {
    return <VerifyGate email={session.user.email} onRefresh={refresh} onLogout={refresh} />
  }

  // 「我的」空间（DESIGN §12，#76）：普通用户整个应用就是它，永远见不到左栏；
  // admin 从左栏顶部的「管理 | 我的」切进来，路径进 /my 即换壳。
  const isAdmin = session.user?.role === 'admin'
  if (session.user && (!isAdmin || loc.pathname === '/my' || loc.pathname.startsWith('/my/'))) {
    return (
      <MySpace
        user={session.user}
        isAdmin={isAdmin}
        build={{ version: session.version, distro: session.distro, admin: isAdmin }}
        onLogout={refresh}
        onRefresh={refresh}
      />
    )
  }

  return (
    <>
      <Shell session={session} onPassword={() => setPwOpen(true)} onLogout={refresh} />
      {pwOpen && <ChangePassword onClose={() => setPwOpen(false)} onChanged={refresh} />}
    </>
  )
}

function Shell({
  session,
  onPassword,
  onLogout,
}: {
  session: SessionState
  onPassword: () => void
  onLogout: () => void
}) {
  const user = session.user
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 管理空间各页统一 wide 档（v0.55）：此前按「吃宽与否」分 920/1320 两档，
  // 切 tab 时画布左右边缘跳来跳去，比省下的留白更扎眼。
  return (
    <TopShell
      // 进得了管理空间就是 admin：纯密码时代的老会话没有 user，也只有管理员拿得到。
      build={{ version: session.version, distro: session.distro, admin: true }}
      tabs={NAV.map((item) => (
        <NavLink key={item.to} to={item.to}>
          {item.label}
        </NavLink>
      ))}
      right={
        <>
          {/* 「管理 ⇄ 我的」两空间切换（DESIGN §12）：两边顶栏右侧各摆对方的
              入口。只有带用户身份的会话才摆——纯密码时代的老会话没有「我的」。 */}
          {user && (
            <NavLink className="btn btn-quiet" to="/my">
              我的
            </NavLink>
          )}
          {/* 设置齿轮（DESIGN §2 v0.71）：只在管理空间；设置面 = 登录与邮件 + 导出 / 导入。 */}
          <button
            type="button"
            className="act-icon topbar-gear"
            aria-label="设置"
            title="设置"
            onClick={() => setSettingsOpen(true)}
          >
            <IconGear />
          </button>
          {settingsOpen && <SettingsDialog onClose={() => setSettingsOpen(false)} />}
          <AvatarMenu user={user}>
            {(close) => (
              <>
                <button
                  type="button"
                  className="menu-item"
                  onClick={() => {
                    close()
                    onPassword()
                  }}
                >
                  修改密码
                </button>
                <button
                  type="button"
                  className="menu-item"
                  onClick={async () => {
                    await api.post('/logout')
                    await onLogout()
                  }}
                >
                  退出登录
                </button>
              </>
            )}
          </AvatarMenu>
        </>
      }
      width="wide"
    >
      <Routes>
        <Route path="/channels" element={<Channels />} />
        {/* 选中的渠道进 URL（口径层 v0.45 主从两栏）：刷新、回退都还留在同一个
            渠道上。`new` 占的是同一段位置——新建时右栏就是那张空表单。 */}
        <Route path="/channels/:id" element={<Channels />} />
        {/* 静态的 /channels/pricing 优先于上面的 /channels/:id：RR 按路径特异性排序，与书写顺序无关。 */}
        <Route path="/channels/pricing" element={<Pricing />} />
        <Route path="/gateway" element={<Keys />} />
        <Route path="/routing" element={<AccessPoints />} />
        <Route path="/usage" element={<Rankings />} />
        <Route path="/usage/logs" element={<Logs />} />
        <Route path="/users" element={<Users />} />
        {/* 旧路径（含历史的 /overview）跳新地址，query 保留。 */}
        {Object.entries(LEGACY_REDIRECTS).map(([from, to]) => (
          <Route key={from} path={from} element={<LegacyRedirect to={to} />} />
        ))}
        {/* 兜住 /panel 本身以及任何不认识的深链接。用 replace 是为了不在
            浏览器历史里留下一个「回退就又跳一次」的空档。 */}
        <Route path="*" element={<Navigate to="/channels" replace />} />
      </Routes>
    </TopShell>
  )
}

function LegacyRedirect({ to }: { to: string }) {
  const { search } = useLocation()
  return <Navigate to={{ pathname: to, search }} replace />
}
