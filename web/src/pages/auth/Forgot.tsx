import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../api'
import { ErrorBar } from '../../ui'
import AuthCard from './AuthCard'

/**
 * 忘记密码（#62 决议 6）。后端对「邮箱不存在」也回 200——所以成功文案说的是
 * 「如果这个邮箱注册过」，页面不知道、也不该知道更多。OAuth-only 账号设密码
 * 也走这一条。
 */
export default function Forgot() {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/password-reset', { email })
      setSent(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthCard title="找回密码">
      {sent ? (
        <>
          <div className="bar bar-ok">
            如果 <b>{email}</b> 注册过，一封重置密码的邮件已经在路上（30 分钟内有效）。
          </div>
          <Link className="btn btn-quiet" to="/">
            回登录页
          </Link>
        </>
      ) : (
        <form className="login-form" onSubmit={submit}>
          <p className="muted login-note">
            输入注册邮箱，我们发一封带重置链接的邮件。没设过密码的账号（OAuth 登录的）也走这里设置密码。
          </p>
          <input
            type="email"
            autoFocus
            autoComplete="username"
            placeholder="邮箱"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <ErrorBar message={error} />
          <button className="btn btn-primary" disabled={busy || !email}>
            {busy ? '发送中…' : '发送重置邮件'}
          </button>
          <div className="login-links">
            <Link to="/">回登录页</Link>
          </div>
        </form>
      )}
    </AuthCard>
  )
}
