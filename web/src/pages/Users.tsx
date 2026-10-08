import { useState } from 'react'
import { api } from '../api'
import type { InviteCode, User } from '../api'
import { Card, Confirm, CopyButton, CopyCode, Dialog, Empty, ErrorBar, Field, fmtMoney, fmtTime, useList } from '../ui'
import { Segmented } from '../fields'

/**
 * 用户页（#72）：用户列表 + 建号逃生门、邀请码。两块各一张卡——
 * 它们是同一件事（谁能进来、怎么进来）的两个把手。登录与邮件配置在右上齿轮的设置面（v0.71）。
 */
export default function Users() {
  const users = useList(() => api.get<User[] | null>('/users'))
  const invites = useList(() => api.get<InviteCode[] | null>('/invite-codes'))
  const [creating, setCreating] = useState(false)
  const [quotaFor, setQuotaFor] = useState<User | null>(null)

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
                <th>月配额</th>
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
                  {/* 月配额（#65/#75）：null = 不限（默认）、0 = 封停、正数 = 每月
                      USD 上限。点开弹框改——三态里有个 null，行内输入框表达不了
                      「清空回不限」。 */}
                  <td>
                    <button className="btn btn-ghost" onClick={() => setQuotaFor(u)}>
                      {u.monthly_quota_usd === null
                        ? '不限'
                        : u.monthly_quota_usd === 0
                          ? '封停'
                          : `${fmtMoney(u.monthly_quota_usd)}/月`}
                    </button>
                  </td>
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

      {creating && (
        <CreateUserDialog
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false)
            void users.reload()
          }}
        />
      )}
      {quotaFor && (
        <QuotaDialog
          user={quotaFor}
          onClose={() => setQuotaFor(null)}
          onSaved={() => {
            setQuotaFor(null)
            void users.reload()
          }}
        />
      )}
    </>
  )
}

/**
 * 调月配额（#65/#75）：留空 = 不限额（null）、0 = 封停、正数 = 每月 USD 上限。
 * 改额立即生效——闸每次请求现算本月已用，当月已用不重算、不清零。
 */
function QuotaDialog({ user, onClose, onSaved }: { user: User; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(user.monthly_quota_usd === null ? '' : String(user.monthly_quota_usd))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const parsed = value.trim() === '' ? null : Number(value)
  const invalid = parsed !== null && (!Number.isFinite(parsed) || parsed < 0)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.put(`/users/${user.id}/quota`, { monthly_quota_usd: parsed })
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={`月配额：${user.display_name || user.email}`} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field
          label="每月上限（USD）"
          hint={invalid ? '要是 0 或正数' : '留空 = 不限额；0 = 封停该账号的转发；按 UTC 自然月，下月自动恢复'}
        >
          <input
            autoFocus
            inputMode="decimal"
            placeholder="不限额"
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </Field>
        {parsed === 0 && (
          <div className="bar bar-warn">0 是封停：这个账号的转发请求会立即全部被拒。</div>
        )}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || invalid}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

function CreateUserDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
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
          <input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field
          label="确认初始密码"
          hint={confirmPw && confirmPw !== password ? '两次输入不一致' : undefined}
        >
          <input
            type="password"
            autoComplete="new-password"
            value={confirmPw}
            onChange={(e) => setConfirmPw(e.target.value)}
          />
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
          <button
            className="btn btn-primary"
            disabled={busy || !email || password.length < 8 || confirmPw !== password}
          >
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
      {fresh && <FreshInvites codes={fresh} onClose={() => setFresh(null)} />}
    </>
  )
}

/**
 * FreshInvites 是邀请码生成后的回执，形制照抄 Keys 的 FreshKey：keybox + CopyButton
 * 幽灵键。多个码一次复制、换行分隔——发给几个人时各拆一行正好。
 */
function FreshInvites({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  return (
    <Dialog title="邀请码已生成" onClose={onClose}>
      <div className="form">
        <p className="muted">一码一人，用后作废。之后在列表里随时能再看到。</p>
        {codes.map((c) => (
          <code key={c} className="keybox">
            {c}
          </code>
        ))}
        <div className="form-actions">
          <CopyButton
            value={codes.join('\n')}
            label={codes.length > 1 ? `复制全部 ${codes.length} 个` : '复制'}
          />
          <button className="btn btn-primary" onClick={onClose}>
            好
          </button>
        </div>
      </div>
    </Dialog>
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
