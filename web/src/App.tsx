import { useCallback, useEffect, useRef, useState } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api, download, importConfig, previewImport, setUnauthorizedHandler } from './api'
import type { SessionState, User } from './api'
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
import { Confirm, Dialog, ErrorBar } from './ui'

// 顶栏 tab 纯文字（v0.54）：左栏时代的六枚线性图标随左栏一起退役——横排里
// 图标+字比纯字更挤，且「我的」空间的 tab 本来就没有图标，统一按无图标走。
const NAV: { to: string; label: string }[] = [
  { to: '/channels', label: '模型' },
  { to: '/keys', label: 'API Key' },
  { to: '/logs', label: '调用记录' },
  { to: '/access-points', label: '接入点' },
  { to: '/rankings', label: '排行' },
  { to: '/pricing', label: '定价' },
  { to: '/users', label: '用户' },
]

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
    return <MySpace user={session.user} isAdmin={isAdmin} onLogout={refresh} onRefresh={refresh} />
  }

  return (
    <>
      <Shell user={session.user} onPassword={() => setPwOpen(true)} onLogout={refresh} />
      {pwOpen && <ChangePassword onClose={() => setPwOpen(false)} onChanged={refresh} />}
    </>
  )
}

/**
 * 把整份业务配置导成 channels.yaml 下载（口径层 §2.9 #32）。
 *
 * 放在头像菜单里而不是某一页里（v0.54 前在左栏底部，理由不变）：它导的是**全部**
 * 业务配置，渠道、接入点、API Key 一份都不落，挂在其中任何一页下面都会读成
 * 「只导这一页的东西」。
 *
 * 沿用当前会话、不再问一次口令——登录者本来就能在页面上逐条看到全部秘密，导出只是
 * 把 1+N 次点击压成一次。失败只有一类（存量 API Key 拿不到原值），报文里点了名，
 * 用弹框而不是左栏里那条窄缝来显示。
 */
function ExportButton() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  return (
    <>
      <button
        type="button"
        className="menu-item"
        disabled={busy}
        title="导出全部业务配置，用于部署一台无管理界面的纯转发实例"
        onClick={async () => {
          setBusy(true)
          try {
            await download('/export', 'channels.yaml')
          } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        {busy ? '导出中…' : '导出配置'}
      </button>
      {error && (
        <Dialog title="导出失败" onClose={() => setError('')}>
          <div className="dialog-note">
            <ErrorBar message={error} />
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={() => setError('')}>
                知道了
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

/**
 * 把一份 channels.yaml 一次性导入并**整份覆盖**当前业务配置（#59）。
 *
 * 与导出并排放在头像菜单里，理由同款：它动的是**全部**业务配置，挂在任何一页下面
 * 都会读成「只导这一页的东西」。
 *
 * 流程（口径层 v1.03，推翻 v0.37 的「先警告后清单」时序）：选文件 → 立即试算 →
 * 确认框直接摆试算结果（对账单：汇总 + 逐行清单）→ 导入。试算与真导入同一条链路，
 * 所以 400 的原文在确认阶段就看全——试算被闸打回时真导入也会被同一道闸打回，确认键
 * 随之整个收起（摆着它就是邀请人去撞闸）。覆盖语义只剩常驻一句；「整份回滚、一次
 * 报全」是后端事务的保证，真导入失败时 400 原文自会说话，不再写进确认框说教。
 * 确认键用 danger 形态：全量覆盖是弹框里唯一主操作，第一眼就该危险，两段式确认的
 * 防误触语义不变。成功弹框缩成一句——清单在确认阶段已经看过，重复摆第二遍是同一份
 * 数据说两遍。成功关框后整页重载：整份配置换掉了，各页面攒着的本地状态全部过期，
 * 重载比逐页打补丁诚实。
 */
type ImportPreview =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | { state: 'ok'; changes: string[] }

// 汇总行的新增/删除计数按清单动词前缀数。清单格式（「新增渠道 X」「删除 API Key Y」）
// 是后端 reconcile 的输出、前后端同仓：后端改动词这里会悄悄归零，别只改一边。
const adds = (changes: string[]) => changes.filter((c) => c.startsWith('新增')).length
const dels = (changes: string[]) => changes.filter((c) => c.startsWith('删除')).length

// 清单按动词分两摞摆（v0.55）：后端 reconcile 的输出按实体类型交错，混排时人得
// 逐行扫动词才拼得出「哪些会没」。删除是覆盖导入里真正危险的那半，组头着警示色。
// 动词对不上号的行（后端加了新动词而这里没跟上）兜进末尾无头组，掉出清单才是事故。
function ImportChanges({ changes }: { changes: string[] }) {
  const added = changes.filter((c) => c.startsWith('新增'))
  const deleted = changes.filter((c) => c.startsWith('删除'))
  const rest = changes.filter((c) => !c.startsWith('新增') && !c.startsWith('删除'))
  const groups = [
    { label: '新增', items: added },
    { label: '删除', items: deleted, danger: true },
    { label: '', items: rest },
  ].filter((g) => g.items.length > 0)
  return (
    <>
      {groups.map((g) => (
        <div className="import-group" key={g.label || '其他'}>
          {g.label && (
            <p className={'import-group-label' + (g.danger ? ' import-group-danger' : '')}>
              {g.label} · {g.items.length}
            </p>
          )}
          <ul className="import-changes">
            {g.items.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
        </div>
      ))}
    </>
  )
}

function ImportButton() {
  const fileRef = useRef<HTMLInputElement>(null)
  const [pending, setPending] = useState<{ name: string; text: string } | null>(null)
  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<string[] | null>(null)

  // 试算是异步的，而选文件可以连着来（试算途中取消、再选另一份）：迟到的响应必须
  // 认得出自己已经过期，否则确认框会摆着 B 的文件名、A 的清单，而按下去导的是 B——
  // 人照着另一份文件的账批准了全量覆盖。每次选文件递一个号，回来对不上就丢掉。
  const previewSeq = useRef(0)

  const close = () => {
    previewSeq.current++
    setPending(null)
    setPreview(null)
    setError('')
  }

  return (
    <>
      <button
        type="button"
        className="menu-item"
        title="导入一份 channels.yaml，整份覆盖当前业务配置"
        onClick={() => fileRef.current?.click()}
      >
        导入配置
      </button>
      <input
        ref={fileRef}
        type="file"
        accept=".yaml,.yml"
        hidden
        onChange={async (e) => {
          const file = e.target.files?.[0]
          // 清掉 value：不清的话「取消后再选同一个文件」不触发 onChange，按钮就哑了。
          e.target.value = ''
          if (!file) return
          // 选完立即试算：确认框里摆的必须是「这份文件对这个库会干什么」的事实，
          // 不是覆盖语义的说教。试算没回来之前确认键不渲染——没算过的账不能签。
          setError('')
          const seq = ++previewSeq.current
          const text = await file.text()
          if (seq !== previewSeq.current) return
          setPending({ name: file.name, text })
          setPreview({ state: 'loading' })
          try {
            const changes = await previewImport(text)
            if (seq === previewSeq.current) setPreview({ state: 'ok', changes })
          } catch (err) {
            if (seq === previewSeq.current) {
              setPreview({ state: 'error', message: err instanceof Error ? err.message : String(err) })
            }
          }
        }}
      />
      {pending && (
        <Dialog title="导入配置" guard scroll onClose={close}>
          <div className="dialog-note">
            {preview?.state === 'loading' && <p>正在试算 {pending.name} 会带来的变更…</p>}
            {preview?.state === 'error' && (
              <>
                <p>
                  试算 <b>{pending.name}</b> 没过，导入不会执行——同一份文件真导入也会被同一道闸打回：
                </p>
                <div className="bar bar-error bar-pre">{preview.message}</div>
              </>
            )}
            {preview?.state === 'ok' && (
              <>
                <p>
                  试算 <b>{pending.name}</b>：
                  {preview.changes.length === 0
                    ? '配置无变化——文件内容与当前配置一致。'
                    : `将新增 ${adds(preview.changes)} 项、删除 ${dels(preview.changes)} 项。`}
                </p>
                {preview.changes.length > 0 && <ImportChanges changes={preview.changes} />}
              </>
            )}
            <p className="muted">文件里没有的一律删除，有的一律按文件覆盖。</p>
            {error && <div className="bar bar-error bar-pre">{error}</div>}
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" disabled={busy} onClick={close}>
                取消
              </button>
              {preview?.state === 'ok' && (
                <Confirm
                  danger
                  label={busy ? '导入中…' : '导入并覆盖'}
                  confirm="确定覆盖当前配置？"
                  onConfirm={async () => {
                    setBusy(true)
                    setError('')
                    try {
                      setDone(await importConfig(pending.text))
                      close()
                    } catch (err) {
                      setError(err instanceof Error ? err.message : String(err))
                    } finally {
                      setBusy(false)
                    }
                  }}
                />
              )}
            </div>
          </div>
        </Dialog>
      )}
      {done && (
        <Dialog title="导入完成" onClose={() => window.location.reload()}>
          <div className="dialog-note">
            <p>{done.length === 0 ? '配置无变化——文件内容与当前配置一致。' : '配置已按文件覆盖。'}</p>
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={() => window.location.reload()}>
                完成
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

function Shell({
  user,
  onPassword,
  onLogout,
}: {
  user?: User
  onPassword: () => void
  onLogout: () => void
}) {
  // 管理空间六页统一 wide 档（v0.55）：此前按「吃宽与否」分 920/1320 两档，
  // 切 tab 时画布左右边缘跳来跳去，比省下的留白更扎眼。
  return (
    <TopShell
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
                <ExportButton />
                <ImportButton />
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
        <Route path="/access-points" element={<AccessPoints />} />
        <Route path="/keys" element={<Keys />} />
        <Route path="/logs" element={<Logs />} />
        <Route path="/rankings" element={<Rankings />} />
        <Route path="/pricing" element={<Pricing />} />
        <Route path="/users" element={<Users />} />
        {/* 概览已从导航拿掉（口径层 v0.75）。老地址与拆页前的 /usage 都落到排行，
            不交给下面那个 `*`：开着旧标签刷新会掉到渠道页上，那不像跳转，像页面没了。 */}
        <Route path="/overview" element={<Navigate to="/rankings" replace />} />
        <Route path="/usage" element={<Navigate to="/rankings" replace />} />
        {/* 兜住 /panel 本身以及任何不认识的深链接。用 replace 是为了不在
            浏览器历史里留下一个「回退就又跳一次」的空档。 */}
        <Route path="*" element={<Navigate to="/channels" replace />} />
      </Routes>
    </TopShell>
  )
}
