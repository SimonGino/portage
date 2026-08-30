import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../../api'
import { ErrorBar } from '../../ui'
import AuthCard from './AuthCard'

/**
 * 重置密码落地页：邮件链接指到 /admin/reset?token=...。成功后后端吊销该用户
 * **全部**会话（#62 决议 6），所以这里的出口只有「去登录」。
 */
export default function Reset() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const [password, setPassword] = useState('')
  const [done, setDone] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/password-reset/confirm', { token, password })
      setDone(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  if (!token) {
    return (
      <AuthCard title="重置密码">
        <div className="bar bar-error">链接不完整：请从邮件里完整复制链接打开。</div>
        <Link className="btn btn-quiet" to="/forgot">
          重新发起找回
        </Link>
      </AuthCard>
    )
  }

  return (
    <AuthCard title="重置密码">
      {done ? (
        <>
          <div className="bar bar-ok">密码已重置，所有旧登录状态已失效。</div>
          <Link className="btn btn-primary" to="/">
            用新密码登录
          </Link>
        </>
      ) : (
        <form className="login-form" onSubmit={submit}>
          <input
            type="password"
            autoFocus
            autoComplete="new-password"
            placeholder="新密码（至少 8 位）"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <ErrorBar message={error} />
          <button className="btn btn-primary" disabled={busy || password.length < 8}>
            {busy ? '提交中…' : '设置新密码'}
          </button>
        </form>
      )}
    </AuthCard>
  )
}
