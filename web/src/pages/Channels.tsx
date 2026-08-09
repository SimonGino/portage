import { useState } from 'react'
import { api, PROTOCOL_LABEL } from '../api'
import type { Channel, Protocol } from '../api'
import { Card, Confirm, Dialog, Empty, ErrorBar, Field, Toggle, useList } from '../ui'

export default function Channels() {
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [credFor, setCredFor] = useState<Channel | null>(null)

  // 任何写操作都走这里：出错就把后端那句话原样显示出来。400 装的是启动闸的
  // 校验原文（「渠道 x 已启用但没有可用凭证」这种），改写成「保存失败」等于
  // 把唯一有用的信息扔掉。写完一律重拉——删渠道会级联带走它的候选，
  // 就地改那一行会让页面和库悄悄分叉。
  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return
    }
    await reload()
  }

  if (loading && data === null) return <div className="boot">加载中…</div>
  const channels = data ?? []

  return (
    <>
      <ErrorBar message={error} />
      <Card
        title="渠道"
        action={
          <button className="btn btn-primary" onClick={() => setEditing('new')}>
            新建渠道
          </button>
        }
      >
        {channels.length === 0 ? (
          <Empty>还没有渠道。先建一个上游，再给它加纳管模型，最后在「接入点」里对外暴露。</Empty>
        ) : (
          <div className="channels">
            {channels.map((ch) => (
              <ChannelCard
                key={ch.id}
                ch={ch}
                onEdit={() => setEditing(ch)}
                onCredential={() => setCredFor(ch)}
                mutate={mutate}
              />
            ))}
          </div>
        )}
      </Card>

      {editing && (
        <ChannelForm
          channel={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void reload()
          }}
        />
      )}
      {credFor && (
        <CredentialForm
          channel={credFor}
          onClose={() => setCredFor(null)}
          onSaved={() => {
            setCredFor(null)
            void reload()
          }}
        /> /* 保存完要重拉：has_credential 从 false 变 true，「缺凭证」那个标记得跟着消失 */
      )}
    </>
  )
}

function ChannelCard({
  ch,
  onEdit,
  onCredential,
  mutate,
}: {
  ch: Channel
  onEdit: () => void
  onCredential: () => void
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [newModel, setNewModel] = useState('')
  const models = ch.models ?? []

  return (
    <div className={'channel' + (ch.disabled ? ' is-off' : '')}>
      <div className="channel-head">
        <div className="channel-title">
          <strong>{ch.name}</strong>
          <span className="tag">{PROTOCOL_LABEL[ch.protocol] ?? ch.protocol}</span>
          {ch.disabled && <span className="tag tag-off">已停用</span>}
          {/* 没凭证的启用渠道会让整个网关启动闸不过（保存时也会被挡），
              所以这条得显眼，不能只是个灰字。 */}
          {!ch.has_credential && <span className="tag tag-warn">缺凭证</span>}
        </div>
        <div className="row-actions">
          <button className="btn btn-quiet" onClick={onCredential}>
            {ch.has_credential ? '换凭证' : '设凭证'}
          </button>
          <button className="btn btn-quiet" onClick={onEdit}>
            编辑
          </button>
          <Confirm ghost onConfirm={() => void mutate(() => api.del(`/channels/${ch.id}`))} />
        </div>
      </div>

      <div className="channel-url">{ch.base_url}</div>

      <div className="models">
        <div className="models-title">纳管模型{models.length > 0 && ` · ${models.length}`}</div>
        {models.length === 0 ? (
          <div className="muted">还没有纳管模型。填上游那边真实的模型名，比如 gpt-4o、deepseek-chat。</div>
        ) : (
          <div className="model-grid">
            {models.map((m) => (
              <div key={m.id} className={'model' + (m.disabled ? ' is-off' : '')}>
                {/* title 补全名：网格单元里长名字是截断的，鼠标停一下能看全 */}
                <code className="model-name" title={m.upstream_model}>
                  {m.upstream_model}
                </code>
                <div className="model-actions">
                  <Toggle
                    on={!m.disabled}
                    onChange={(on) =>
                      void mutate(() => api.put(`/channel-models/${m.id}`, { disabled: !on }))
                    }
                  />
                  <Confirm
                    ghost
                    onConfirm={() => void mutate(() => api.del(`/channel-models/${m.id}`))}
                  />
                </div>
              </div>
            ))}
          </div>
        )}
        <form
          className="inline-form"
          onSubmit={(e) => {
            e.preventDefault()
            const v = newModel.trim()
            if (!v) return
            setNewModel('')
            void mutate(() => api.post(`/channels/${ch.id}/models`, { upstream_model: v }))
          }}
        >
          <input
            placeholder="上游模型名"
            value={newModel}
            onChange={(e) => setNewModel(e.target.value)}
          />
          <button className="btn btn-quiet" disabled={!newModel.trim()}>
            添加
          </button>
        </form>
      </div>
    </div>
  )
}

const PROTOCOLS: Protocol[] = ['anthropic', 'openai_cc', 'openai_responses']

function ChannelForm({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(channel?.name ?? '')
  const [proto, setProto] = useState<Protocol>(channel?.protocol ?? 'openai_cc')
  const [baseURL, setBaseURL] = useState(channel?.base_url ?? '')
  const [disabled, setDisabled] = useState(channel?.disabled ?? false)
  // 凭证只在**新建**时出现在这张表单里。编辑走单独的入口，这样「改个名字」
  // 不可能顺手把凭证清空——后端的修改接口本来就不看这个字段。
  const [credential, setCredential] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const body = { name, protocol: proto, base_url: baseURL, disabled }
      if (channel) {
        await api.put(`/channels/${channel.id}`, body)
      } else {
        await api.post('/channels', { ...body, credential })
      }
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={channel ? `编辑渠道：${channel.name}` : '新建渠道'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <Field label="渠道名" hint="自己认的名字，会出现在调用流水里">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="上游协议">
          <select value={proto} onChange={(e) => setProto(e.target.value as Protocol)}>
            {PROTOCOLS.map((p) => (
              <option key={p} value={p}>
                {PROTOCOL_LABEL[p]}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Base URL" hint="到版本段为止，例如 https://api.example.com/v1">
          <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
        </Field>
        {!channel && (
          <Field label="上游凭证" hint="只写不回读：保存之后页面上再也看不到它，只能整把换掉">
            <input
              type="password"
              autoComplete="off"
              value={credential}
              onChange={(e) => setCredential(e.target.value)}
            />
          </Field>
        )}
        <label className="check">
          <input type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} />
          停用这个渠道
        </label>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !name.trim()}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

function CredentialForm({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel
  onClose: () => void
  onSaved: () => void
}) {
  const [credential, setCredential] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  return (
    <Dialog title={`设置凭证：${channel.name}`} onClose={onClose}>
      <form
        className="form"
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          try {
            await api.put(`/channels/${channel.id}/credential`, { credential })
            onSaved()
          } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="bar bar-warn">
          上游凭证只写不回读，服务端不会把它发回来。这里填的会<strong>替换</strong>当前那把。
        </div>
        <Field label="上游 API key">
          <input
            type="password"
            autoFocus
            autoComplete="off"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
          />
        </Field>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !credential.trim()}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
