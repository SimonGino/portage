import { useState } from 'react'
import { api } from '../api'
import type { AuthSettings, InviteCode, User } from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, fmtTime, useList } from '../ui'
import { Segmented } from '../fields'

/**
 * 用户页（#72）：用户列表 + 建号逃生门、邀请码、登录与邮件配置。三块各一张卡——
 * 它们是同一件事（谁能进来、怎么进来）的三个把手。
 */
export default function Users() {
  const users = useList(() => api.get<User[] | null>('/users'))
  const invites = useList(() => api.get<InviteCode[] | null>('/invite-codes'))
  const [creating, setCreating] = useState(false)

  // 任免/停用共用的提交壳：护栏（最后一个启用的 admin 不许降/停）长在服务端，
  // 这里只负责把那句 400 原文摆出来。
  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      users.setError('')
    } catch (e) {
      users.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await users.reload()
  }

  return (
    <>
      <ErrorBar message={users.error} />
      <Card
        title="用户"
        action={
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            添加用户
          </button>
        }
      >
        {/* 「添加用户」是 SMTP 未配时的逃生门（#62 决议 3）：admin 面对面发号，
            号一出生就是已验证。 */}
        {(users.data ?? []).length === 0 ? (
          <Empty>还没有用户。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>邮箱</th>
                <th>名字</th>
                <th>角色</th>
                <th>登录方式</th>
                <th>状态</th>
                <th>创建时间</th>
                <th className="col-actions" />
              </tr>
            </thead>
            <tbody>
              {(users.data ?? []).map((u) => (
                <tr key={u.id} className={u.disabled ? 'is-off' : ''}>
                  <td>{u.email}</td>
                  <td>{u.display_name || <span className="muted">—</span>}</td>
                  <td>{u.role === 'admin' ? '管理员' : '用户'}</td>
                  <td className="muted">{u.has_password ? '密码' : '仅 OAuth'}</td>
                  <td>
                    {u.disabled ? (
                      <span className="muted">已停用</span>
                    ) : u.email_verified ? (
                      '正常'
                    ) : (
                      <span className="muted">待验证邮箱</span>
                    )}
                  </td>
                  <td className="muted">{fmtTime(u.created_at)}</td>
                  {/* 任免 + 停用（#61：admin 可任免，多 admin 允许）。两个动作都过
                      Confirm——降级/停用即时生效（角色与冻结都是每请求联查），没有
                      「保存前反悔」的缓冲。最后一个启用的 admin 的护栏在服务端，
                      被拦时那句 400 会出现在页顶的 ErrorBar 里。 */}
                  <td className="col-actions">
                    <div className="row-actions">
                      {u.role === 'admin' ? (
                        <Confirm
                          ghost
                          label="降为用户"
                          confirm="确定收回管理员权限？"
                          onConfirm={() => void mutate(() => api.put(`/users/${u.id}/role`, { role: 'user' }))}
                        />
                      ) : (
                        <Confirm
                          ghost
                          label="设为管理员"
                          confirm="确定给予全部管理权限？"
                          onConfirm={() => void mutate(() => api.put(`/users/${u.id}/role`, { role: 'admin' }))}
                        />
                      )}
                      {u.disabled ? (
                        <button
                          className="btn btn-ghost"
                          onClick={() => void mutate(() => api.put(`/users/${u.id}/disabled`, { disabled: false }))}
                        >
                          启用
                        </button>
                      ) : (
                        <Confirm
                          ghost
                          label="停用"
                          confirm="确定停用？其会话与访问立即冻结"
                          onConfirm={() => void mutate(() => api.put(`/users/${u.id}/disabled`, { disabled: true }))}
                        />
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <InviteSection invites={invites} />
      <AuthSettingsSection />

      {creating && (
        <CreateUserDialog
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false)
            void users.reload()
          }}
        />
      )}
    </>
  )
}

function CreateUserDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState<'user' | 'admin'>('user')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/users', { email, password, display_name: displayName, role })
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="添加用户" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <p className="muted">
          直接建号是没配邮件时的发号通道：邮箱归属由你当面担保，号一出生就算已验证。
        </p>
        <Field label="邮箱">
          <input
            type="email"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field label="初始密码" hint="至少 8 位，发给对方后建议其自行修改">
          <input type="text" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <Field label="名字" hint="可留空">
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
        <Field label="角色" hint="管理员能改全部配置，普通用户只有自己的面板">
          <Segmented
            value={role}
            options={[
              { value: 'user', label: '用户' },
              { value: 'admin', label: '管理员' },
            ]}
            onChange={setRole}
          />
        </Field>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !email || password.length < 8}>
            {busy ? '创建中…' : '创建'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

// ── 邀请码 ──────────────────────────────────────────────────────────────

function InviteSection({ invites }: { invites: ReturnType<typeof useList<InviteCode[] | null>> }) {
  const [generating, setGenerating] = useState(false)
  const [fresh, setFresh] = useState<string[] | null>(null)

  async function revoke(id: number) {
    try {
      await api.del(`/invite-codes/${id}`)
      invites.setError('')
    } catch (e) {
      invites.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await invites.reload()
  }

  const list = invites.data ?? []
  const now = Math.floor(Date.now() / 1000)

  return (
    <>
      <ErrorBar message={invites.error} />
      <Card
        title="邀请码"
        action={
          <button className="btn btn-primary" onClick={() => setGenerating(true)}>
            生成邀请码
          </button>
        }
      >
        {list.length === 0 ? (
          <Empty>还没有邀请码。注册必须持码——生成后发给要请进来的人。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>邀请码</th>
                <th>状态</th>
                <th>有效期</th>
                <th>生成时间</th>
                <th className="col-actions" />
              </tr>
            </thead>
            <tbody>
              {list.map((ic) => {
                const used = ic.used_by_email !== ''
                const expired = !used && ic.expires_at !== null && ic.expires_at <= now
                return (
                  <tr key={ic.id} className={used || expired ? 'is-off' : ''}>
                    <td>
                      <CopyCode value={ic.code} />
                    </td>
                    <td>
                      {used ? (
                        <>
                          已被 <b>{ic.used_by_email}</b> 使用
                        </>
                      ) : expired ? (
                        <span className="muted">已过期</span>
                      ) : (
                        '未使用'
                      )}
                    </td>
                    <td className="muted">
                      {ic.expires_at === null
                        ? '不过期'
                        : new Date(ic.expires_at * 1000).toLocaleString()}
                    </td>
                    <td className="muted">{fmtTime(ic.created_at)}</td>
                    <td className="col-actions">
                      {/* 已用的码不能撤销：它的价值只剩「记录谁用的」。 */}
                      {!used && (
                        <div className="row-actions">
                          <Confirm ghost onConfirm={() => void revoke(ic.id)} />
                        </div>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>
      {generating && (
        <GenerateInviteDialog
          onClose={() => setGenerating(false)}
          onDone={(codes) => {
            setGenerating(false)
            setFresh(codes)
            void invites.reload()
          }}
        />
      )}
      {fresh && (
        <Dialog title="邀请码已生成" onClose={() => setFresh(null)}>
          <div className="form">
            <p className="muted">一码一人，用后作废。之后在列表里随时能再看到。</p>
            {fresh.map((c) => (
              <code key={c} className="keybox">
                {c}
              </code>
            ))}
            <div className="form-actions">
              <button className="btn btn-primary" onClick={() => setFresh(null)}>
                好
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

function GenerateInviteDialog({
  onClose,
  onDone,
}: {
  onClose: () => void
  onDone: (codes: string[]) => void
}) {
  const [count, setCount] = useState('1')
  const [hours, setHours] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const res = await api.post<{ codes: string[] }>('/invite-codes', {
        count: Number(count) || 1,
        expires_in_hours: Number(hours) || 0,
      })
      onDone(res.codes)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="生成邀请码" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field label="数量" hint="一次最多 100 个">
          <input inputMode="numeric" value={count} onChange={(e) => setCount(e.target.value)} />
        </Field>
        <Field label="有效期（小时）" hint="留空或 0 = 不过期">
          <input
            inputMode="numeric"
            placeholder="不过期"
            value={hours}
            onChange={(e) => setHours(e.target.value)}
          />
        </Field>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy}>
            {busy ? '生成中…' : '生成'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

// ── 登录与邮件配置 ──────────────────────────────────────────────────────

const ENCRYPTION_OPTIONS = [
  { value: 'starttls', label: 'STARTTLS', hint: '587' },
  { value: 'ssl', label: 'SSL', hint: '465' },
  { value: 'none', label: '不加密', hint: '仅内网' },
] as const

type Encryption = (typeof ENCRYPTION_OPTIONS)[number]['value']

/**
 * 登录与邮件配置（#62 决议 7）：站点外部 URL、SMTP、两家 OAuth client。改完即生效。
 *
 * secret 三样（SMTP 密码、client_secret）**从不回显**，页面只知道「设没设」。
 * 表单里的 secret 输入框留空 = 保留现值（不发这个字段），要清空得点「清除」。
 */
function AuthSettingsSection() {
  const settings = useList(() => api.get<AuthSettings>('/auth-settings'))
  if (settings.loading && settings.data === null) return null
  if (settings.data === null) return <ErrorBar message={settings.error} />
  return <AuthSettingsForm initial={settings.data} reload={settings.reload} />
}

function AuthSettingsForm({ initial, reload }: { initial: AuthSettings; reload: () => Promise<void> }) {
  const [siteURL, setSiteURL] = useState(initial.site_url)
  const [smtp, setSmtp] = useState({
    host: initial.smtp.host,
    port: initial.smtp.port,
    encryption: (initial.smtp.encryption || 'starttls') as Encryption,
    username: initial.smtp.username,
    from: initial.smtp.from,
  })
  // null = 没动过（不发字段，后端保留现值）；空串 = 明确清空。
  const [smtpPassword, setSmtpPassword] = useState<string | null>(null)
  const [github, setGithub] = useState({ id: initial.github.client_id, secret: null as string | null })
  const [google, setGoogle] = useState({ id: initial.google.client_id, secret: null as string | null })
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setSaved(false)
    setError('')
    try {
      await api.put('/auth-settings', {
        site_url: siteURL,
        smtp: {
          host: smtp.host,
          port: smtp.port,
          encryption: smtp.encryption,
          username: smtp.username,
          from: smtp.from,
          ...(smtpPassword !== null ? { password: smtpPassword } : {}),
        },
        github: { client_id: github.id, ...(github.secret !== null ? { secret: github.secret } : {}) },
        google: { client_id: google.id, ...(google.secret !== null ? { secret: google.secret } : {}) },
      })
      setSaved(true)
      setSmtpPassword(null)
      setGithub((g) => ({ ...g, secret: null }))
      setGoogle((g) => ({ ...g, secret: null }))
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card
      title="登录与邮件"
      action={
        <button className="btn btn-quiet" onClick={() => setTesting(true)}>
          发测试邮件
        </button>
      }
    >
      <form className="form" onSubmit={save}>
        <Field
          label="站点外部 URL"
          hint="邮件里的验证/重置链接、OAuth 回调地址都从它拼；不配则注册与 OAuth 都开不了"
        >
          <input
            placeholder="https://portage.example.com"
            value={siteURL}
            onChange={(e) => setSiteURL(e.target.value)}
          />
        </Field>

        <h3 className="form-section">SMTP 发信</h3>
        <p className="muted">
          未配 SMTP 时注册入口关闭，验证信与找回密码也发不出去——那时发号走上面的「添加用户」。
        </p>
        <div className="form-grid">
          <Field label="服务器">
            <input
              placeholder="smtp.example.com"
              value={smtp.host}
              onChange={(e) => setSmtp({ ...smtp, host: e.target.value })}
            />
          </Field>
          <Field label="端口" hint="留空按加密方式取默认">
            <input
              inputMode="numeric"
              placeholder={smtp.encryption === 'ssl' ? '465' : smtp.encryption === 'none' ? '25' : '587'}
              value={smtp.port}
              onChange={(e) => setSmtp({ ...smtp, port: e.target.value })}
            />
          </Field>
        </div>
        <Field label="加密方式">
          <Segmented
            value={smtp.encryption}
            options={[...ENCRYPTION_OPTIONS]}
            onChange={(v) => setSmtp({ ...smtp, encryption: v })}
          />
        </Field>
        <div className="form-grid">
          <Field label="用户名" hint="留空 = 匿名发信">
            <input value={smtp.username} onChange={(e) => setSmtp({ ...smtp, username: e.target.value })} />
          </Field>
          <SecretField
            label="密码"
            set={initial.smtp.password_set}
            value={smtpPassword}
            onChange={setSmtpPassword}
          />
        </div>
        <Field label="发件地址">
          <input
            placeholder="noreply@example.com"
            value={smtp.from}
            onChange={(e) => setSmtp({ ...smtp, from: e.target.value })}
          />
        </Field>

        <h3 className="form-section">GitHub 登录</h3>
        <OAuthClientFields
          provider="GitHub"
          callback={initial.callback_urls.github}
          clientID={github.id}
          onClientID={(v) => setGithub({ ...github, id: v })}
          secretSet={initial.github.secret_set}
          secret={github.secret}
          onSecret={(v) => setGithub({ ...github, secret: v })}
        />

        <h3 className="form-section">Google 登录</h3>
        {/* Google 上游硬限 HTTPS、禁裸 IP：内网 http 部署注册不了 client，这不是网关能
            绕的——只配 GitHub 或完全不配 OAuth 都是设计内的形态。 */}
        <OAuthClientFields
          provider="Google"
          callback={initial.callback_urls.google}
          clientID={google.id}
          onClientID={(v) => setGoogle({ ...google, id: v })}
          secretSet={initial.google.secret_set}
          secret={google.secret}
          onSecret={(v) => setGoogle({ ...google, secret: v })}
        />

        {saved && !error && <div className="bar bar-ok">已保存，即刻生效。</div>}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button className="btn btn-primary" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
      {testing && <TestEmailDialog onClose={() => setTesting(false)} />}
    </Card>
  )
}

/** secret 输入框：从不回显现值，只标「已设置」。留空 = 保留，点清除 = 置空。 */
function SecretField({
  label,
  set,
  value,
  onChange,
}: {
  label: string
  set: boolean
  value: string | null
  onChange: (v: string | null) => void
}) {
  const cleared = value === ''
  return (
    <Field
      label={label}
      hint={set && !cleared ? '已设置——留空保留现值，输入即替换' : cleared ? '将清除' : '未设置'}
    >
      <div className="secret-row">
        <input
          type="password"
          autoComplete="new-password"
          placeholder={set && !cleared ? '••••••••' : ''}
          value={value ?? ''}
          onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}
        />
        {set && !cleared && (
          <button type="button" className="btn btn-quiet" onClick={() => onChange('')}>
            清除
          </button>
        )}
      </div>
    </Field>
  )
}

function OAuthClientFields({
  provider,
  callback,
  clientID,
  onClientID,
  secretSet,
  secret,
  onSecret,
}: {
  provider: string
  callback: string
  clientID: string
  onClientID: (v: string) => void
  secretSet: boolean
  secret: string | null
  onSecret: (v: string | null) => void
}) {
  return (
    <>
      <div className="form-grid">
        <Field label="Client ID">
          <input value={clientID} onChange={(e) => onClientID(e.target.value)} />
        </Field>
        <SecretField label="Client Secret" set={secretSet} value={secret} onChange={onSecret} />
      </div>
      <Field label="回调地址" hint={`在 ${provider} 注册 OAuth 应用时填这个（先配好站点外部 URL）`}>
        {callback ? <CopyCode value={callback} /> : <span className="muted">先在上面配站点外部 URL</span>}
      </Field>
    </>
  )
}

function TestEmailDialog({ onClose }: { onClose: () => void }) {
  const [to, setTo] = useState('')
  const [error, setError] = useState('')
  const [ok, setOk] = useState(false)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setOk(false)
    try {
      await api.post('/auth-settings/test-email', { to })
      setOk(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="发测试邮件" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <p className="muted">
          用当前<b>已保存</b>的 SMTP 配置发一封测试信——改了配置先保存再测。
        </p>
        <Field label="收件地址">
          <input type="email" autoFocus value={to} onChange={(e) => setTo(e.target.value)} />
        </Field>
        {ok && <div className="bar bar-ok">已发出。收到它说明 SMTP 配置可用。</div>}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            关闭
          </button>
          <button className="btn btn-primary" disabled={busy || !to}>
            {busy ? '发送中…' : '发送'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
