import { useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_PATH, PROTOCOL_SHORT } from '../api'
import type { Channel, ProbeResult, Protocol } from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, Toggle, useList } from '../ui'
import { SegmentedMulti } from '../fields'
import { Avatar, ChannelIcon, ModelIcon, vendorForChannel, vendorForModel } from '../icons'

export default function Channels() {
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [credFor, setCredFor] = useState<Channel | null>(null)
  // 探测结果只活在这个组件的内存里：口径层 v0.33 定的是「只提示、不落库、不参与
  // 路由」——探测结果会过期，存下来就变成一份会撒谎的缓存。刷新页面它就该没了。
  const [probes, setProbes] = useState<Record<number, ProbeResult[] | 'running'>>({})

  async function probe(id: number) {
    setProbes((p) => ({ ...p, [id]: 'running' }))
    try {
      const r = await api.post<{ results: ProbeResult[] }>(`/channels/${id}/probe`)
      setProbes((p) => ({ ...p, [id]: r.results }))
    } catch {
      // 探测失败不算保存失败，也不该盖掉页面上别的错误：静默丢掉那一格。
      setProbes((p) => {
        const next = { ...p }
        delete next[id]
        return next
      })
    }
  }

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
                onProbe={() => void probe(ch.id)}
                probe={probes[ch.id]}
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
          onSaved={(id) => {
            setEditing(null)
            void reload()
            // 保存成功之后才探测，且不挡保存——勾错协议集的后果（一半端点全 404、
            // 另一半完全正常）启动闸看不见，人正好在这一刻最有可能改对它。
            void probe(id)
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
  onProbe,
  probe,
  mutate,
}: {
  ch: Channel
  onEdit: () => void
  onCredential: () => void
  onProbe: () => void
  probe?: ProbeResult[] | 'running'
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const models = ch.models ?? []
  const protos = ch.protocols ?? []
  const unreachable = Array.isArray(probe) ? probe.filter((r) => !r.reachable) : []

  return (
    <div className={'channel' + (ch.disabled ? ' is-off' : '')}>
      <div className="channel-head">
        <div className="channel-title">
          <ChannelIcon channel={ch} size={24} />
          <strong>{ch.name}</strong>
          {/* 协议集全列出来：这一格回答的是「这个渠道能接住哪些客户端」，
              只显示一个就看不出来 Responses 的 harness 会不会走转换。 */}
          {protos.map((p) => (
            <span key={p} className="tag" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
              {PROTOCOL_SHORT[p] ?? p}
            </span>
          ))}
          {protos.length === 0 && <span className="tag tag-warn">协议集为空</span>}
          {ch.disabled && <span className="tag tag-off">已停用</span>}
          {/* 没凭证的启用渠道会让整个网关启动闸不过（保存时也会被挡），
              所以这条得显眼，不能只是个灰字。 */}
          {!ch.has_credential && <span className="tag tag-warn">缺凭证</span>}
        </div>
        <div className="row-actions">
          <button className="btn btn-quiet" onClick={onProbe} disabled={probe === 'running'}>
            {probe === 'running' ? '探测中…' : '探测协议'}
          </button>
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

      {/* 探测结论只提示，不挡任何操作，也不落库——它会过期（口径层 v0.33）。
          全通就报一句就好，不通的逐条列出来。 */}
      {Array.isArray(probe) && (
        <div className={'probe' + (unreachable.length > 0 ? ' probe-bad' : '')}>
          {unreachable.length === 0 ? (
            <span>探测通过：勾选的 {probe.length} 个协议子路径上游都有</span>
          ) : (
            <>
              <span>
                探测未通过 {unreachable.length} 项——只是提示，不影响保存与路由，但这些协议的客户端打过来会
                404：
              </span>
              <ul>
                {unreachable.map((r) => (
                  <li key={r.protocol}>
                    <code>{PROTOCOL_PATH[r.protocol] ?? r.protocol}</code> {r.detail}
                    {r.status > 0 && ` (HTTP ${r.status})`}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}

      <div className="models">
        <div className="models-title">纳管模型{models.length > 0 && ` · ${models.length}`}</div>
        {models.length === 0 ? (
          <div className="muted">还没有纳管模型。填上游那边真实的模型名，比如 gpt-4o、deepseek-chat。</div>
        ) : (
          <div className="model-grid">
            {models.map((m) => (
              <div key={m.id} className={'model' + (m.disabled ? ' is-off' : '')}>
                <ModelIcon model={m.upstream_model} size={18} />
                {/* 摆的是裸模型名（网格单元里放不下限定名），复制走的是限定名——
                    那才是客户端 `model` 字段要填的东西（口径层 v0.32），手抄一个带
                    斜杠的长串很容易漏字符，漏了的表现是 404。停用的不给复制：抄走
                    了也调不通，title 里给全名就够。 */}
                {m.disabled ? (
                  <code className="model-name" title={m.upstream_model}>
                    {m.upstream_model}
                  </code>
                ) : (
                  <CopyCode
                    className="model-name"
                    value={`${ch.name}/${m.upstream_model}`}
                    label={m.upstream_model}
                    title={`点击复制 ${ch.name}/${m.upstream_model}`}
                  />
                )}
                <div className="model-actions">
                  {m.disabled && <span className="tag tag-off">已停用</span>}
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
  onSaved: (id: number) => void
}) {
  const [name, setName] = useState(channel?.name ?? '')
  // 支持协议集（口径层 v0.33）。默认只勾 CC：绝大多数上游只提供它，多勾一个探测
  // 不过反而要人回来改。
  const [protos, setProtos] = useState<Protocol[]>(
    channel?.protocols?.length ? channel.protocols : ['openai_cc'],
  )
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
      const body = { name, protocols: protos, base_url: baseURL, disabled }
      if (channel) {
        await api.put(`/channels/${channel.id}`, body)
        onSaved(channel.id)
      } else {
        const created = await api.post<{ id: number }>('/channels', { ...body, credential })
        onSaved(created.id)
      }
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

        <Field label="渠道名" hint="会出现在调用流水里，也是限定名的前半截（如 bailian/qwen3-max）。不能含 `/`——模型名那半截本来就可能带，两边都带就分不清界在哪">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field
          label="支持的上游协议"
          hint="这个上游能说的都勾上——同一个账号同时提供 CC 与 Responses 是常态，不必拆成两个渠道。选哪个由客户端打的端点决定：能透传就透传，说不了才转换"
        >
          <SegmentedMulti
            value={protos}
            onChange={setProtos}
            options={PROTOCOLS.map((p) => ({
              value: p,
              label: PROTOCOL_LABEL[p],
              hint: PROTOCOL_PATH[p],
            }))}
          />
        </Field>
        {/* base_url 存的是「协议子路径之前」的前缀，上面那几个子路径由网关自己接。
            这是必踩的坑：填成 …/v1 会拼出 /v1/v1/chat/completions。 */}
        <Field
          label="Base URL"
          hint="填到协议子路径之前，网关自己接后缀。OpenAI 官方是 https://api.openai.com（不带 /v1），百炼是 https://dashscope.aliyuncs.com/compatible-mode"
        >
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
