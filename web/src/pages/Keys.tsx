import { useState } from 'react'
import { api } from '../api'
import type { AccessPoint, ApiKey } from '../api'
import { Card, Confirm, Dialog, Empty, ErrorBar, Field, Toggle, fmtTime, useList } from '../ui'

export default function Keys() {
  const keys = useList(() => api.get<ApiKey[] | null>('/keys'))
  // 接入点列表用来给白名单当选项：白名单里写的是**接入点名**，
  // 让人手打很容易打错一个字，而打错的表现是那把 key 静默 403。
  const aps = useList(() => api.get<AccessPoint[] | null>('/access-points'))
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<ApiKey | null>(null)
  const [fresh, setFresh] = useState('')

  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      keys.setError('')
    } catch (e) {
      keys.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await keys.reload()
  }

  if (keys.loading && keys.data === null) return <div className="boot">加载中…</div>
  const list = keys.data ?? []
  const apNames = (aps.data ?? []).map((a) => a.model)

  return (
    <>
      <ErrorBar message={keys.error} />
      <Card
        title="网关 key"
        action={
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            新建 key
          </button>
        }
      >
        <p className="muted">
          客户端拿这个 key 调网关（<code>x-api-key</code> 或 <code>Authorization: Bearer</code>）。
          它跟上游凭证是两回事，也跟管理端密码是两回事。
        </p>
        {list.length === 0 ? (
          <Empty>还没有 key。没有 key 的话所有转发请求都会回 401。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>可访问接入点</th>
                <th>创建时间</th>
                <th>启用</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((k) => (
                <tr key={k.id} className={k.disabled ? 'is-off' : ''}>
                  <td>{k.name}</td>
                  <td>
                    {k.allowed_models === '*' ? (
                      <span className="muted">不限</span>
                    ) : (
                      k.allowed_models.split(',').map((m) => (
                        <code key={m} className="chip">
                          {m}
                        </code>
                      ))
                    )}
                  </td>
                  <td className="muted">{fmtTime(k.created_at)}</td>
                  <td>
                    <Toggle
                      on={!k.disabled}
                      onChange={(on) =>
                        void mutate(() =>
                          api.put(`/keys/${k.id}`, {
                            name: k.name,
                            allowed_models: k.allowed_models,
                            disabled: !on,
                          }),
                        )
                      }
                    />
                  </td>
                  {/* 按钮包一层 div：直接把 display:flex 挂在 td 上，这一格就脱离了
                      表格的列模型，渲染出来会跑到卡片外面去。 */}
                  <td>
                    <div className="row-actions">
                      <button className="btn btn-quiet" onClick={() => setEditing(k)}>
                        编辑
                      </button>
                      <Confirm onConfirm={() => void mutate(() => api.del(`/keys/${k.id}`))} />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {creating && (
        <KeyForm
          k={null}
          accessPoints={apNames}
          onClose={() => setCreating(false)}
          onSaved={(plain) => {
            setCreating(false)
            if (plain) setFresh(plain)
            void keys.reload()
          }}
        />
      )}
      {editing && (
        <KeyForm
          k={editing}
          accessPoints={apNames}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void keys.reload()
          }}
        />
      )}
      {fresh && <FreshKey value={fresh} onClose={() => setFresh('')} />}
    </>
  )
}

/**
 * FreshKey 是新 key 的**唯一一次**露面。
 *
 * 服务端只存 key 的哈希，不留明文，所以没有「再看一次」的接口——关掉这个框
 * 就真的没了。这一点必须在界面上说清楚，不然人会以为哪里还能翻出来。
 */
function FreshKey({ value, onClose }: { value: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return (
    <Dialog title="新 key 已生成" onClose={onClose}>
      <div className="form">
        <div className="bar bar-warn">
          现在就复制走。服务端只存哈希，关掉这个框之后**再也看不到**，忘了只能删掉重发一把。
        </div>
        <code className="keybox">{value}</code>
        <div className="form-actions">
          <button
            className="btn btn-quiet"
            onClick={async () => {
              // clipboard API 在非 HTTPS 的非 localhost 页面上是不可用的
              // （局域网访问就是这种情况），失败了别报错，让人手动选中复制。
              try {
                await navigator.clipboard.writeText(value)
                setCopied(true)
              } catch {
                setCopied(false)
              }
            }}
          >
            {copied ? '已复制' : '复制'}
          </button>
          <button className="btn btn-primary" onClick={onClose}>
            我已保存
          </button>
        </div>
      </div>
    </Dialog>
  )
}

function KeyForm({
  k,
  accessPoints,
  onClose,
  onSaved,
}: {
  k: ApiKey | null
  accessPoints: string[]
  onClose: () => void
  onSaved: (plain?: string) => void
}) {
  const [name, setName] = useState(k?.name ?? '')
  const [unlimited, setUnlimited] = useState((k?.allowed_models ?? '*') === '*')
  const [picked, setPicked] = useState<string[]>(
    k && k.allowed_models !== '*' ? k.allowed_models.split(',') : [],
  )
  const [disabled, setDisabled] = useState(k?.disabled ?? false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // 白名单里可能留着已经删掉的接入点名，那种也要显示出来——它是这把 key
  // 当前真实的限制，藏起来会让人看着「不限」实际上是限死的。
  const options = Array.from(new Set([...accessPoints, ...picked]))

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const allowed = unlimited ? '*' : picked.join(',')
      if (k) {
        await api.put(`/keys/${k.id}`, { name, allowed_models: allowed, disabled })
        onSaved()
      } else {
        const res = await api.post<{ id: number; key: string }>('/keys', {
          name,
          allowed_models: allowed,
        })
        onSaved(res.key)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={k ? `编辑 key：${k.name}` : '新建 key'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field label="名称" hint="会出现在调用流水里，用来分辨是哪台机器在调">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="可访问接入点">
          <label className="check">
            <input
              type="checkbox"
              checked={unlimited}
              onChange={(e) => setUnlimited(e.target.checked)}
            />
            不限（所有接入点）
          </label>
          {!unlimited &&
            (options.length === 0 ? (
              <span className="field-hint">还没有接入点可选。不勾任何一个等于不限。</span>
            ) : (
              <div className="checks">
                {options.map((m) => (
                  <label key={m} className="check">
                    <input
                      type="checkbox"
                      checked={picked.includes(m)}
                      onChange={(e) =>
                        setPicked((prev) =>
                          e.target.checked ? [...prev, m] : prev.filter((x) => x !== m),
                        )
                      }
                    />
                    <code>{m}</code>
                  </label>
                ))}
              </div>
            ))}
        </Field>
        {k && (
          <label className="check">
            <input
              type="checkbox"
              checked={disabled}
              onChange={(e) => setDisabled(e.target.checked)}
            />
            停用这把 key
          </label>
        )}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !name.trim()}>
            {busy ? '保存中…' : k ? '保存' : '生成'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
