import { useState } from 'react'
import { useLocation } from 'react-router-dom'
import { api } from '../../api'
import type { AuthConfig, OAuthIdentity, User } from '../../api'
import { Card, Confirm, ErrorBar, Field, fmtTime, useList } from '../../ui'
import ChangePassword from '../ChangePassword'

/**
 * 「账号」页（DESIGN §12）：邮箱（只读）、展示名、改密码、OAuth 绑定/解绑。
 * OAuth 绑定的收尾 302 带着 oauth_linked / oauth_error 落回来，这页把回执摆出来。
 */
export default function MyAccount({ user, onRefresh }: { user: User; onRefresh: () => void }) {
  const loc = useLocation()
  const params = new URLSearchParams(loc.search)
  const linked = params.get('oauth_linked')
  const oauthError = params.get('oauth_error')

  return (
    <>
      {linked && <div className="bar bar-ok">已绑定 {linked} 登录。</div>}
      {oauthError && <div className="bar bar-error">{oauthError}</div>}
      <ProfileSection user={user} onRefresh={onRefresh} />
      <PasswordSection user={user} onRefresh={onRefresh} />
      <IdentitiesSection />
    </>
  )
}

function ProfileSection({ user, onRefresh }: { user: User; onRefresh: () => void }) {
  const [name, setName] = useState(user.display_name)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setSaved(false)
    setError('')
    try {
      await api.put('/account/profile', { display_name: name })
      setSaved(true)
      onRefresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="账号">
      <form className="form" onSubmit={save}>
        <Field label="邮箱" hint="登录标识，不可修改">
          <input value={user.email} disabled />
        </Field>
        <Field label="展示名" hint="可留空；页面上代替邮箱显示">
          <input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        {saved && !error && <div className="bar bar-ok">已保存。</div>}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button className="btn btn-primary" disabled={busy || name === user.display_name}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Card>
  )
}

function PasswordSection({ user, onRefresh }: { user: User; onRefresh: () => void }) {
  const [changing, setChanging] = useState(false)
  return (
    <Card
      title="密码"
      action={
        user.has_password ? (
          <button className="btn btn-quiet" onClick={() => setChanging(true)}>
            修改密码
          </button>
        ) : undefined
      }
    >
      {user.has_password ? (
        <p className="muted">改完所有登录状态一起失效，需要用新密码重新登录。</p>
      ) : (
        // OAuth-only 账号没有旧密码可验，设密码走「忘记密码」的邮件链路（#62 决议 5）。
        <p className="muted">
          这个账号目前只用 OAuth 登录。要设一个密码，去登录页走「忘记密码」，用邮件链接设置。
        </p>
      )}
      {changing && <ChangePassword onClose={() => setChanging(false)} onChanged={onRefresh} />}
    </Card>
  )
}

function IdentitiesSection() {
  const identities = useList(() => api.get<OAuthIdentity[] | null>('/account/identities'))
  // 有哪些门可绑看 auth-config：管理员没配的家不摆绑定按钮——摆一个必然失败的
  // 按钮是在骗人点。
  const cfg = useList(() => api.get<AuthConfig>('/auth-config'))

  const bound = identities.data ?? []
  const available = (cfg.data?.oauth ?? []).filter((p) => !bound.some((b) => b.provider === p))

  async function unlink(provider: string) {
    try {
      await api.del(`/account/identities/${provider}`)
      identities.setError('')
    } catch (e) {
      identities.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await identities.reload()
  }

  if (bound.length === 0 && available.length === 0) return null

  return (
    <Card title="OAuth 登录">
      <ErrorBar message={identities.error} />
      {bound.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>已绑定</th>
              <th>绑定时间</th>
              <th className="col-actions" />
            </tr>
          </thead>
          <tbody>
            {bound.map((b) => (
              <tr key={b.provider}>
                <td>{b.provider}</td>
                <td className="muted">{fmtTime(b.created_at)}</td>
                <td className="col-actions">
                  <div className="row-actions">
                    {/* 最后一条登录通道不许拆的护栏在服务端，被拦时 400 原文进上面的
                        ErrorBar。 */}
                    <Confirm
                      ghost
                      label="解绑"
                      confirm={`确定解绑 ${b.provider}？`}
                      onConfirm={() => void unlink(b.provider)}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {available.length > 0 && (
        <div className="row-actions">
          {available.map((p) => (
            // 浏览器导航不是 fetch：绑定要跟着 302 去上游授权页。
            <a key={p} className="btn btn-quiet" href={`/panel/oauth/${p}/start?mode=link`}>
              绑定 {p}
            </a>
          ))}
        </div>
      )}
    </Card>
  )
}
