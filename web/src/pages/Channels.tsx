import { useState } from 'react'
import { api, PROTOCOL_LABEL } from '../api'
import type { Channel, Protocol } from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, Toggle, useList } from '../ui'
import { Segmented } from '../fields'
import { Avatar, ChannelIcon, ModelIcon, vendorForChannel, vendorForModel } from '../icons'

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
          <Empty>还没有渠道。先建一个上游，再给它加纳管模型——加完就能直接调了。</Empty>
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
  const models = ch.models ?? []

  return (
    <div className={'channel' + (ch.disabled ? ' is-off' : '')}>
      <div className="channel-head">
        <ChannelIcon channel={ch} size={32} />
        <div className="channel-id">
          <div className="channel-name">
            <strong>{ch.name}</strong>
            <span className="tag">{PROTOCOL_LABEL[ch.protocol] ?? ch.protocol}</span>
            {ch.disabled && <span className="tag tag-off">已停用</span>}
            {/* 没凭证的启用渠道会让整个网关启动闸不过（保存时也会被挡），
                所以这条得显眼，不能只是个灰字。 */}
            {!ch.has_credential && <span className="tag tag-warn">缺凭证</span>}
          </div>
          <div className="channel-url">{ch.base_url}</div>
        </div>
        <div className="row-actions">
          <button className="btn btn-quiet" onClick={onCredential}>
            {ch.has_credential ? '换凭证' : '设凭证'}
          </button>
          <button className="btn btn-quiet" onClick={onEdit}>
            编辑
          </button>
          <Confirm onConfirm={() => void mutate(() => api.del(`/channels/${ch.id}`))} />
        </div>
      </div>

      <div className="models">
        <div className="models-title">
          纳管模型
          <span className="muted">
            {models.length > 0 && ` · ${models.length} 个`}
          </span>
        </div>
        {models.length === 0 ? (
          <div className="muted">还没有纳管模型。填上游那边真实的模型名，比如 gpt-4o、deepseek-chat。</div>
        ) : (
          <ul className="model-list">
            {models.map((m) => (
              <li key={m.id} className={m.disabled ? 'is-off' : ''}>
                <ModelIcon model={m.upstream_model} size={18} />
                <code className="model-name">{m.upstream_model}</code>
                {/* 限定名是客户端 `model` 字段真正要填的东西（口径层 v0.32），
                    所以摆出来而且能一键复制——手抄一个带斜杠的长串很容易漏字符，
                    漏了的表现是 404。停用的不给复制：抄走了也调不通。 */}
                {m.disabled ? (
                  <span className="tag tag-off">已停用</span>
                ) : (
                  <CopyCode value={`${ch.name}/${m.upstream_model}`} title="客户端 model 字段填这个" />
                )}
                <span className="spacer" />
                <Toggle
                  on={!m.disabled}
                  onChange={(on) =>
                    void mutate(() => api.put(`/channel-models/${m.id}`, { disabled: !on }))
                  }
                />
                <Confirm onConfirm={() => void mutate(() => api.del(`/channel-models/${m.id}`))} />
              </li>
            ))}
          </ul>
        )}
        <AddModels channel={ch} mutate={mutate} />
      </div>
    </div>
  )
}

/**
 * AddModels 是往渠道里加纳管模型的那一行。
 *
 * 接受**一次粘一批**——逗号、空格、换行都算分隔。上游控制台的模型列表复制下来就是
 * 这种形状，逐个敲进去要来回十几趟。已经纳管过的自动跳过而不是报错：粘一份完整清单
 * 进来「把新的加上」是最常见的用法，为几个重复项整批失败没有道理。
 */
function AddModels({
  channel,
  mutate,
}: {
  channel: Channel
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [draft, setDraft] = useState('')
  const existing = new Set((channel.models ?? []).map((m) => m.upstream_model))

  const parsed = Array.from(
    new Set(
      draft
        .split(/[\s,，、]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  )
  const fresh = parsed.filter((m) => !existing.has(m))
  const dupes = parsed.length - fresh.length

  return (
    <form
      className="add-models"
      onSubmit={(e) => {
        e.preventDefault()
        if (fresh.length === 0) return
        setDraft('')
        void mutate(async () => {
          // 串行而不是 Promise.all：SQLite 那头连接池是 1，并发写只会排队，
          // 而串行出错时能停在第一个失败上，不至于半成功一片。
          for (const m of fresh) {
            await api.post(`/channels/${channel.id}/models`, { upstream_model: m })
          }
        })
      }}
    >
      <div className="add-models-row">
        <Avatar
          vendor={fresh.length === 1 ? vendorForModel(fresh[0]) : null}
          fallback={fresh.length === 1 ? fresh[0] : '+'}
          size={20}
        />
        <input
          placeholder="上游模型名，可一次粘一批（逗号或换行分隔）"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <button className="btn btn-quiet" disabled={fresh.length === 0}>
          {fresh.length > 1 ? `添加 ${fresh.length} 个` : '添加'}
        </button>
      </div>
      {fresh.length > 1 && (
        <div className="add-models-preview">
          {fresh.map((m) => (
            <span key={m} className="chip">
              <ModelIcon model={m} size={16} />
              <code>{m}</code>
            </span>
          ))}
        </div>
      )}
      {dupes > 0 && <div className="field-hint">其中 {dupes} 个已经纳管过，会跳过。</div>}
    </form>
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
        {/* 图标是从 base_url 的 host 猜出来的（渠道没有「供应商」这个字段）。
            边填边显示，等于顺手校验了域名有没有填错——图标一直是首字母块，
            多半是 base_url 还没填对。 */}
        <div className="form-preview">
          <Avatar vendor={vendorForChannel({ name, base_url: baseURL })} fallback={name || '?'} size={40} />
          <div>
            <div className="form-preview-name">{name || '未命名渠道'}</div>
            <div className="muted">{baseURL || '还没填 base_url'}</div>
          </div>
        </div>

        <Field label="渠道名" hint="会出现在调用流水里，也是限定名的前半截（如 bailian/qwen3-max）">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="上游协议" hint="决定网关跟这个上游怎么说话；与客户端用什么协议无关，对不上会走转换">
          <Segmented
            value={proto}
            onChange={setProto}
            options={PROTOCOLS.map((p) => ({ value: p, label: PROTOCOL_LABEL[p] }))}
          />
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
          上游凭证只写不回读，服务端不会把它发回来。这里填的会**替换**当前那把。
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
