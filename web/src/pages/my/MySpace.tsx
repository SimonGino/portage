import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api } from '../../api'
import type { User } from '../../api'
import { AvatarMenu, TopShell } from '../../topshell'
import LogsView from '../logs/view'
import MyKeys from './MyKeys'
import MyUsage from './MyUsage'
import MyModels from './MyModels'
import MyAccount from './MyAccount'
import { QuotaChip, useQuota } from './quota'

/**
 * 「我的」空间（DESIGN §12，#76）：与管理空间共用同一只顶栏壳（v0.54），
 * tab 换成本人五页，右侧多一枚配额微条；主区默认 760 的单列文档流。普通用户
 * 登录后直接落进来；admin 从管理空间顶栏的「我的」切进来，这边给「管理」回程。
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
  // 调用记录与用量两页放宽（v0.53）：十来列的流水表与环形图 + 节律带的两栏
  // 排布在 760px 里挤不开；其余页照旧单列窄排。
  const wide = loc.pathname === '/my/logs' || loc.pathname === '/my/usage'

  return (
    <TopShell
      tabs={
        <>
          <NavLink to="/my" end>
            我的 Key
          </NavLink>
          {/* 调用记录与用量与配额是管理端两页的本人形态（v0.53 拆五 tab）：流水
              从「用量与配额」页尾拆出来独立成 tab，那页原地升级成排行形态。 */}
          <NavLink to="/my/logs">调用记录</NavLink>
          <NavLink to="/my/usage">用量与配额</NavLink>
          <NavLink to="/my/models">模型</NavLink>
          <NavLink to="/my/account">账号</NavLink>
        </>
      }
      right={
        <>
          <QuotaChip quota={quota.data} />
          {isAdmin && (
            <NavLink className="btn btn-quiet" to="/channels">
              管理
            </NavLink>
          )}
          {/* 菜单里只有退出：改密码、邮箱、OAuth 绑定都住在「账号」页，这里
              再摆一份就是同一件事两个入口。 */}
          <AvatarMenu user={user}>
            {(close) => (
              <button
                type="button"
                className="menu-item"
                onClick={() => {
                  close()
                  void api.post('/logout').then(onLogout, onLogout)
                }}
              >
                退出登录
              </button>
            )}
          </AvatarMenu>
        </>
      }
      width={wide ? 'wide' : 'narrow'}
    >
      <Routes>
        <Route path="/my" element={<MyKeys quota={quota} />} />
        <Route path="/my/logs" element={<LogsView mine />} />
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
    </TopShell>
  )
}
