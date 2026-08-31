import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api } from '../../api'
import type { User } from '../../api'
import { PortageMark } from '../../brand'
import MyKeys from './MyKeys'
import MyUsage from './MyUsage'
import MyModels from './MyModels'
import MyAccount from './MyAccount'
import { QuotaChip, useQuota } from './quota'

/**
 * 「我的」空间壳（DESIGN §12，#76）：无左栏，顶栏 = 品牌 + 四个 tab + 右侧配额
 * 微条与头像；主区单列文档流（约 760px 居中）。普通用户登录后直接落进来，永远
 * 见不到左栏；admin 从左栏顶部的「管理 | 我的」切进来，顶栏右侧有「管理」回程。
 */
export default function MySpace({
  user,
  isAdmin,
  onLogout,
  onRefresh,
}: {
  user: User
  /** admin 逛自己的空间时顶栏给「管理」回程；普通用户没有这一格。 */
  isAdmin: boolean
  onLogout: () => void
  onRefresh: () => void
}) {
  const quota = useQuota()
  const loc = useLocation()

  return (
    <div className="myspace">
      <header className="mytop">
        <span className="brand">
          <PortageMark size={20} />
          <b>Portage</b>
        </span>
        <nav className="mytabs" aria-label="我的空间">
          <NavLink to="/my" end>
            我的 Key
          </NavLink>
          <NavLink to="/my/usage">用量与配额</NavLink>
          <NavLink to="/my/models">模型</NavLink>
          <NavLink to="/my/account">账号</NavLink>
        </nav>
        <div className="mytop-right">
          <QuotaChip quota={quota.data} />
          {isAdmin && (
            <NavLink className="btn btn-quiet" to="/channels">
              管理
            </NavLink>
          )}
          <div className="acct-avatar" title={user.email} aria-hidden>
            {(user.display_name || user.email)[0].toUpperCase()}
          </div>
          <button
            type="button"
            className="btn btn-quiet"
            onClick={() => void api.post('/logout').then(onLogout, onLogout)}
          >
            退出
          </button>
        </div>
      </header>

      <main className="mymain">
        <Routes>
          <Route path="/my" element={<MyKeys quota={quota} />} />
          <Route path="/my/usage" element={<MyUsage quota={quota} />} />
          <Route path="/my/models" element={<MyModels />} />
          <Route path="/my/account" element={<MyAccount user={user} onRefresh={onRefresh} />} />
          {/* OAuth 绑定的收尾 302 落在 /panel/ 根上、结论在 query 里（oauth_linked /
              oauth_error）——带着这两个参数的兜底进账号页，别把回执甩在 Key 页上。 */}
          <Route
            path="*"
            element={
              <Navigate
                to={{
                  pathname:
                    loc.search.includes('oauth_linked') || loc.search.includes('oauth_error')
                      ? '/my/account'
                      : '/my',
                  search: loc.search,
                }}
                replace
              />
            }
          />
        </Routes>
      </main>
    </div>
  )
}
