import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../api'
import type { AuthConfig } from '../api'
import { PortageMark } from '../brand'
import { ErrorBar } from '../ui'

const PROVIDER_LABEL: Record<string, string> = { github: 'GitHub', google: 'Google' }

export default function Login({
  passwordSet,
  onLoggedIn,
}: {
  passwordSet: boolean
  onLoggedIn: () => void
}) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  // auth-config 挂了也别拦登录：null 时只是不画注册/OAuth 那几扇门。
  const [cfg, setCfg] = useState<AuthConfig | null>(null)
  const loc = useLocation()
  const nav = useNavigate()
  // OAuth 回调失败是 302 带 query 回到这里的（浏览器导航没有别处能放话）。
  const params = new URLSearchParams(loc.search)
  const oauthError = params.get('oauth_error') ?? ''

  useEffect(() => {
    api.get<AuthConfig>('/auth-config').then(setCfg, () => setCfg(null))
  }, [])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/login', { email, password })
      // 把 oauth_error 之类的残留 query 清掉：登录成功后它已经没有对象了。
      if (loc.search) nav(loc.pathname, { replace: true })
      onLoggedIn()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="login" onSubmit={submit}>
        <h1>
          <PortageMark size={20} />
          Portage
        </h1>
        {/* 「还没设密码」跟「密码错了」要分开说：前者的补救是去改配置重启，
            后者是再输一次。含糊成一句「登录失败」会让人对着配置文件反复重试。 */}
        {!passwordSet ? (
          <div className="bar bar-warn">
            尚未设置管理密码。在 config.yaml 里填 <code>admin_password</code>，
            或给容器设环境变量 <code>PORTAGE_ADMIN_PASSWORD</code>，然后重启。
          </div>
        ) : (
          <>
            {oauthError && <div className="bar bar-error">{oauthError}</div>}
            <input
              type="email"
              autoFocus
              autoComplete="username"
              placeholder="邮箱"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <input
              type="password"
              autoComplete="current-password"
              placeholder="密码"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <ErrorBar message={error} />
            <button className="btn btn-primary" disabled={busy || !email || !password}>
              {busy ? '登录中…' : '登录'}
            </button>
            {/* OAuth 是浏览器导航不是 fetch：start 会 302 去上游授权页。 */}
            {cfg && cfg.oauth.length > 0 && (
              <div className="login-oauth">
                {cfg.oauth.map((p) => (
                  <a key={p} className="btn btn-quiet" href={`/panel/oauth/${p}/start`}>
                    用 {PROVIDER_LABEL[p] ?? p} 登录
                  </a>
                ))}
              </div>
            )}
            <div className="login-links">
              {cfg?.registration_open && <Link to="/register">邀请码注册</Link>}
              <Link to="/forgot">忘记密码</Link>
            </div>
            {/* 注册入口关闭时说清找谁，而不是让人以为自己点坏了什么。 */}
            {cfg && !cfg.registration_open && cfg.registration_closed_reason && (
              <p className="muted login-note">{cfg.registration_closed_reason}</p>
            )}
          </>
        )}
      </form>
    </div>
  )
}
