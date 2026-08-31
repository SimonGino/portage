import { useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api } from '../../api'
import { ErrorBar } from '../../ui'
import AuthCard from './AuthCard'

const PROVIDER_LABEL: Record<string, string> = { github: 'GitHub', google: 'Google' }

/**
 * OAuth 完成注册页（#62 决议 5）：首登无匹配账号时，回调把身份装进接力 token
 * 302 到这里——显示拿到的邮箱，补一个邀请码。邀请码填错时后端会随 400 发一个
 * **新 token**（旧的已消费），页面悄悄换上它让人原地重试，不用重走整趟 OAuth。
 */
export default function OAuthComplete({ onDone }: { onDone: () => void }) {
  const nav = useNavigate()
  const [params] = useSearchParams()
  const [token, setToken] = useState(params.get('token') ?? '')
  const [pending, setPending] = useState<{ provider: string; email: string } | null>(null)
  const [dead, setDead] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!token) return
    // pending 只读不消费：这一眼不烧 token，刷新页面无害。
    api.get<{ provider: string; email: string }>(`/oauth/pending?token=${encodeURIComponent(token)}`).then(
      setPending,
      (err: unknown) => setDead(err instanceof Error ? err.message : String(err)),
    )
    // token 换新（邀请码填错那条路）不必重读 pending——身份没变。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    // 不走 api.post：400 的响应体里除了 error 还有换发的新 token，ApiError 只带得动文案。
    try {
      const res = await fetch('/panel/api/oauth/complete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, invite_code: code }),
        credentials: 'same-origin',
      })
      const payload = (await res.json().catch(() => null)) as {
        error?: string
        token?: string
      } | null
      if (res.ok) {
        // 回到根再刷会话：停在 /oauth-complete 上的话，App 的门外路由还会把这页再画一遍。
        nav('/', { replace: true })
        onDone()
        return
      }
      if (payload?.token) setToken(payload.token)
      setError(payload?.error ?? `请求失败（HTTP ${res.status}）`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  if (!token || dead) {
    return (
      <AuthCard title="完成注册">
        <div className="bar bar-error">{dead || '链接不完整：重新用上游账号登录一次。'}</div>
        <Link className="btn btn-quiet" to="/">
          回登录页
        </Link>
      </AuthCard>
    )
  }

  return (
    <AuthCard title="完成注册">
      {pending === null ? (
        <p className="muted login-note">加载中…</p>
      ) : (
        <form className="login-form" onSubmit={submit}>
          <p className="login-note">
            你的 {PROVIDER_LABEL[pending.provider] ?? pending.provider} 账号（<b>{pending.email}</b>）
            还没注册过。填一个邀请码即可完成注册——邮箱已由上游验证，不用再收验证信。
          </p>
          <input
            autoFocus
            placeholder="邀请码"
            value={code}
            onChange={(e) => setCode(e.target.value)}
          />
          <ErrorBar message={error} />
          <button className="btn btn-primary" disabled={busy || !code.trim()}>
            {busy ? '提交中…' : '完成注册'}
          </button>
          <div className="login-links">
            <Link to="/">取消，回登录页</Link>
          </div>
        </form>
      )}
    </AuthCard>
  )
}
