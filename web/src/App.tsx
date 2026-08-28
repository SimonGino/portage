import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { api, download, importConfig, setUnauthorizedHandler } from './api'
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
import { Confirm, Dialog, ErrorBar } from './ui'

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

/**
 * 把整份业务配置导成 channels.yaml 下载（口径层 §2.9 #32）。
 *
 * 放在左栏底部而不是某一页里：它导的是**全部**业务配置，渠道、接入点、API Key 一份
 * 都不落，挂在其中任何一页下面都会读成「只导这一页的东西」。
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
          <ErrorBar message={error} />
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" onClick={() => setError('')}>
              知道了
            </button>
          </div>
        </Dialog>
      )}
    </>
  )
}

/**
 * 把一份 channels.yaml 一次性导入并**整份覆盖**当前业务配置（#59）。
 *
 * 与导出并排放在左栏底部，理由同款：它动的是全部业务配置，挂在任何一页下面都会
 * 读成「只导这一页的东西」。
 *
 * 流程：选文件 → 覆盖警告（后果写在确认键上）→ 提交 → 变更清单。失败整份回滚、
 * 一次报全——400 的报文是后端写给人看的多行原文，pre-wrap 原样显示，不折行揉成一段。
 * 成功关框后整页重载：整份配置换掉了，各页面攒着的本地状态全部过期，重载比逐页
 * 打补丁诚实。
 */
function ImportButton() {
  const fileRef = useRef<HTMLInputElement>(null)
  const [pending, setPending] = useState<{ name: string; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [changes, setChanges] = useState<string[] | null>(null)

  return (
    <>
      <button type="button" title="导入一份 channels.yaml，整份覆盖当前业务配置" onClick={() => fileRef.current?.click()}>
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
          setError('')
          setPending({ name: file.name, text: await file.text() })
        }}
      />
      {pending && (
        <Dialog title="导入配置" guard onClose={() => setPending(null)}>
          <p>
            将把 <b>{pending.name}</b> 的内容整份覆盖当前业务配置：文件里没有的渠道、接入点、API Key
            会被<b>删除</b>。校验不过会整份回滚、一次报全，库里不会留下半截。
          </p>
          {error && <div className="bar bar-error bar-pre">{error}</div>}
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" disabled={busy} onClick={() => setPending(null)}>
              取消
            </button>
            <Confirm
              label={busy ? '导入中…' : '导入并覆盖'}
              confirm="确定覆盖当前配置？"
              onConfirm={async () => {
                setBusy(true)
                setError('')
                try {
                  setChanges(await importConfig(pending.text))
                  setPending(null)
                } catch (err) {
                  setError(err instanceof Error ? err.message : String(err))
                } finally {
                  setBusy(false)
                }
              }}
            />
          </div>
        </Dialog>
      )}
      {changes && (
        <Dialog title="导入完成" onClose={() => window.location.reload()}>
          {changes.length === 0 ? (
            <p>配置无变化——文件内容与当前配置一致。</p>
          ) : (
            <ul className="import-changes">
              {changes.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          )}
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" onClick={() => window.location.reload()}>
              完成
            </button>
          </div>
        </Dialog>
      )}
    </>
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
            <ExportButton />
            <ImportButton />
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
