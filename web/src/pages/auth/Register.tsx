import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../../api'
import type { AuthConfig } from '../../api'
import { ErrorBar } from '../../ui'
import AuthCard from './AuthCard'

/**
 * 邀请码注册（#62 决议 1/2）：码 + 邮箱 + 密码。成功后后端直接发会话——
 * onDone 让 App 重新问 /session，未验证的号会落到去验证页上。
 */
export default function Register({ onDone }: { onDone: () => void }) {
  const nav = useNavigate()
  const [cfg, setCfg] = useState<AuthConfig | null>(null)
  const [code, setCode] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.get<AuthConfig>('/auth-config').then(setCfg, () => setCfg(null))
  }, [])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/register', { invite_code: code, email, password })
      // 回到根再刷会话：停在 /register 上的话，App 的门外路由还会把这页再画一遍。
      nav('/', { replace: true })
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  // 注册入口关闭（SMTP 未配，#62 决议 3）：页面直说找谁，不摆一张必然失败的表单。
  if (cfg && !cfg.registration_open) {
    return (
      <AuthCard title="注册">
        <div className="bar bar-warn">
          {cfg.registration_closed_reason ?? '注册暂不可用，请联系管理员'}
        </div>
        <Link className="btn btn-quiet" to="/">
          回登录页
        </Link>
      </AuthCard>
    )
  }

  return (
    <AuthCard title="注册">
      <form className="login-form" onSubmit={submit}>
        <input
          autoFocus
          placeholder="邀请码"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
        <input
          type="email"
          autoComplete="username"
          placeholder="邮箱"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <input
          type="password"
          autoComplete="new-password"
          placeholder="密码（至少 8 位）"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <ErrorBar message={error} />
        <button className="btn btn-primary" disabled={busy || !code || !email || password.length < 8}>
          {busy ? '注册中…' : '注册'}
        </button>
        <div className="login-links">
          <Link to="/">已有账号？去登录</Link>
        </div>
      </form>
    </AuthCard>
  )
}
