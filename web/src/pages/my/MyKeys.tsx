import { useState } from 'react'
import { api } from '../../api'
import type { ApiKey, MyModel } from '../../api'
import { Card, Confirm, CopyButton, Dialog, Empty, ErrorBar, Field, SecretValue, Toggle, fmtTime, useList } from '../../ui'
import { Chips } from '../../fields'
import type { Option } from '../../fields'
import { ModelIcon } from '../../icons'
import { QuotaCard, type QuotaHook } from './quota'

/**
 * 「我的 Key」页（DESIGN §12）：配额卡置顶 + 本人 key 表。走 /my/keys 那组接口
 * ——归属焊死在服务端，这页永远只有自己的 key。白名单可自设（#63：自我约束工具
 * 不是权限边界），建议项来自「模型」页同一份可路由清单。
 */
export default function MyKeys({ quota }: { quota: QuotaHook }) {
  const keys = useList(() => api.get<ApiKey[] | null>('/my/keys'))
  const models = useList(() => api.get<{ models: MyModel[] | null }>('/my/models'))
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
  const suggestions: Option<string>[] = (models.data?.models ?? []).map((m) => ({
    value: m.id,
    label: m.id,
    icon: <ModelIcon model={m.id} size={16} />,
  }))

  return (
    <>
      <ErrorBar message={keys.error} />
      <QuotaCard quota={quota.data} />
      <Card
        title="我的 Key"
        action={
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            新建 API Key
          </button>
        }
      >
        {list.length === 0 ? (
          <Empty>还没有 API Key。建一把才能调用网关。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>API Key</th>
                <th>可访问模型</th>
                <th>创建时间</th>
                <th>状态</th>
                <th className="col-actions" />
              </tr>
            </thead>
            <tbody>
              {list.map((k) => (
                <tr key={k.id} className={k.disabled ? 'is-off' : ''}>
                  <td>{k.name}</td>
                  {/* 明文可复制（v0.47）。mine 恒真（这页只有自己的），空串只剩一个
                      意思：加 key_plain 之前建的存量——哈希不可逆，只能删了重建。 */}
                  <td>
                    <SecretValue value={k.key} empty="原值没存过，只能删了重建" />
                  </td>
                  <td>
                    {k.allowed_models === '*' ? (
                      <span className="muted">不限</span>
                    ) : (
                      k.allowed_models.split(',').map((m) => (
                        <span key={m} className="chip">
                          <ModelIcon model={m} size={14} />
                          <code>{m}</code>
                        </span>
                      ))
                    )}
                  </td>
                  <td className="muted">{fmtTime(k.created_at)}</td>
                  <td>
                    <Toggle
                      on={!k.disabled}
                      label={k.name}
                      onChange={(on) =>
                        void mutate(() =>
                          api.put(`/my/keys/${k.id}`, {
                            name: k.name,
                            allowed_models: k.allowed_models,
                            disabled: !on,
                          }),
                        )
                      }
                    />
                  </td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button className="btn btn-quiet" onClick={() => setEditing(k)}>
                        编辑
                      </button>
                      <Confirm ghost onConfirm={() => void mutate(() => api.del(`/my/keys/${k.id}`))} />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {(creating || editing) && (
        <MyKeyForm
          k={editing}
          suggestions={suggestions}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
          onSaved={(plain) => {
            setCreating(false)
            setEditing(null)
            if (plain) setFresh(plain)
            void keys.reload()
          }}
        />
      )}
      {fresh && (
        <Dialog title="新 API Key 已生成" onClose={() => setFresh('')}>
          <div className="form">
            <p className="muted">复制走就能用。这把之后在列表里随时能再看到、再复制。</p>
            <code className="keybox">{fresh}</code>
            <div className="form-actions">
              <CopyButton value={fresh} />
              <button className="btn btn-primary" onClick={() => setFresh('')}>
                我已保存
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

function MyKeyForm({
  k,
  suggestions,
  onClose,
  onSaved,
}: {
  k: ApiKey | null
  suggestions: Option<string>[]
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

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const allowed = unlimited ? '*' : picked.join(',')
      if (k) {
        await api.put(`/my/keys/${k.id}`, { name, allowed_models: allowed, disabled })
        onSaved()
      } else {
        const res = await api.post<{ id: number; key: string }>('/my/keys', {
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
    <Dialog title={k ? `编辑 API Key：${k.name}` : '新建 API Key'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field label="名称" hint="会出现在你的调用流水里，用来分辨是哪台机器在调">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="可访问模型" hint="给这把 key 自设的白名单；逐项精确匹配，不支持通配">
          <label className="check">
            <input
              type="checkbox"
              checked={unlimited}
              onChange={(e) => setUnlimited(e.target.checked)}
            />
            不限（所有可路由的模型）
          </label>
          {!unlimited && (
            <>
              <Chips
                items={picked}
                onChange={setPicked}
                placeholder="点下面的建议，或直接输入后回车"
                suggestions={suggestions}
                renderIcon={(m) => <ModelIcon model={m} size={14} />}
              />
              {picked.length === 0 && (
                <span className="field-hint">一个都不选等于不限——后端把空白名单当作 `*`。</span>
              )}
            </>
          )}
        </Field>
        {k && (
          <label className="check">
            <input
              type="checkbox"
              checked={disabled}
              onChange={(e) => setDisabled(e.target.checked)}
            />
            停用这把 API Key
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
