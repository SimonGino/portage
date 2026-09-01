import { useState } from 'react'
import { api } from '../api'
import type { AccessPoint, ApiKey, Channel } from '../api'
import { Card, Confirm, CopyButton, Dialog, Empty, ErrorBar, Field, SecretValue, Toggle, fmtTime, useList } from '../ui'
import { Chips } from '../fields'
import type { Option } from '../fields'
import { ModelIcon } from '../icons'

export default function Keys() {
  const keys = useList(() => api.get<ApiKey[] | null>('/keys'))
  // 接入点与渠道都要拉：白名单里能写的是**客户端 model 字段那个字符串**，接入点名
  // 和纳管模型限定名两种都算（口径层 v0.32）。手打很容易错一个字，而错了的表现是
  // 那把 key 静默 403。
  const aps = useList(() => api.get<AccessPoint[] | null>('/access-points'))
  const channels = useList(() => api.get<Channel[] | null>('/channels'))
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
  // 可选项 = 未停用的接入点名 + 可用纳管模型的限定名，与 `GET /v1/models` 列的
  // 那份清单同构（口径层 v0.32：两者都列、都可路由）。
  const suggestions: Option<string>[] = [
    ...(aps.data ?? [])
      .filter((a) => !a.disabled)
      .map((a) => ({ value: a.model, label: a.model, icon: <ModelIcon model={a.model} size={16} /> })),
    ...(channels.data ?? [])
      .filter((ch) => !ch.disabled)
      .flatMap((ch) =>
        (ch.models ?? [])
          .filter((m) => !m.disabled)
          .map((m) => {
            const q = `${ch.name}/${m.upstream_model}`
            return { value: q, label: q, icon: <ModelIcon model={q} size={16} /> }
          }),
      ),
  ]

  return (
    <>
      {/* 接入点那一路的报错也要露出来：白名单的可选项全靠它，
          悄悄空掉的话看起来像「一个接入点都没建」。 */}
      <ErrorBar message={keys.error || aps.error || channels.error} />
      <Card
        title="API Key"
        action={
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            新建 API Key
          </button>
        }
      >
        {list.length === 0 ? (
          <Empty>还没有 API Key。一把都没有的话，所有转发请求都会回 401。</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>归属</th>
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
                  {/* 归属列（#73）。无主 = 声明文件所建、还没被启动认领的 key，
                      不是坏数据，如实写。 */}
                  <td>{k.owner || <span className="muted">无主</span>}</td>
                  {/* 明文（v0.47）。空串有两个意思，靠 mine 分辨（#73）：他人的 key
                      后端根本不下发明文（仅主人可见），不是「原值丢了」；mine 且空
                      才是加 key_plain 之前建的那些——哈希不可逆，只能删了重建。 */}
                  <td>
                    {k.mine ? (
                      <SecretValue value={k.key} empty="原值没存过，只能删了重建" />
                    ) : (
                      <span className="muted">仅 key 主人可见</span>
                    )}
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
                  <td className="col-actions">
                    <div className="row-actions">
                      {/* 他人的 key 只做元数据治理（#63）：停用（上面的开关）与删除。
                          编辑动的是名字与白名单，那是 key 主人的自我管理面，按钮
                          整个不出——出一个必然 403 的按钮是在骗人点。 */}
                      {k.mine && (
                        <button className="btn btn-quiet" onClick={() => setEditing(k)}>
                          编辑
                        </button>
                      )}
                      <Confirm ghost onConfirm={() => void mutate(() => api.del(`/keys/${k.id}`))} />
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
          suggestions={suggestions}
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
          suggestions={suggestions}
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
 * FreshKey 是新 key 生成后的回执。
 *
 * v0.47 之前这是它唯一一次露面（服务端只留哈希）；现在列表里随时看得到，所以这个框
 * 只剩「拿去用」这一个作用，那句「关掉就再也看不到」的警告也跟着撤了——留着就是撒谎。
 */
function FreshKey({ value, onClose }: { value: string; onClose: () => void }) {
  return (
    <Dialog title="新 API Key 已生成" onClose={onClose}>
      <div className="form">
        <p className="muted">复制走就能用。这把之后在列表里随时能再看到、再复制。</p>
        <code className="keybox">{value}</code>
        <div className="form-actions">
          <CopyButton value={value} />
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
    <Dialog title={k ? `编辑 API Key：${k.name}` : '新建 API Key'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field label="名称" hint="会出现在调用流水里，用来分辨是哪台机器在调">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field
          label="可访问模型"
          hint="接入点名和纳管模型限定名（渠道名/模型名）都能写；逐项精确匹配，不支持通配"
        >
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
              {/* 已经删掉的名字也照样显示：它是这把 key 当前真实的限制，藏起来会让人
                  看着「限了几个」实际上限的是一堆不存在的名字、等于全锁死。 */}
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
