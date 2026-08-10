import { useState } from 'react'
import {
  api,
  KEY_MODE_OPTIONS,
  PROTOCOL_LABEL,
  PROTOCOL_PATH,
  PROTOCOL_SHORT,
  PROTOCOL_SOON,
} from '../api'
import type { Channel, Credential, KeyMode, ProbeGroup, Protocol } from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, Toggle, useList } from '../ui'
import { Segmented, SegmentedMulti } from '../fields'
import { Avatar, ChannelIcon, ModelIcon, vendorForChannel, vendorForModel } from '../icons'

export default function Channels() {
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [credFor, setCredFor] = useState<Channel | null>(null)
  // 探测结果只活在这个组件的内存里：口径层 v0.33 定的是「只提示、不落库、不参与
  // 路由」——探测结果会过期，存下来就变成一份会撒谎的缓存。刷新页面它就该没了。
  const [probes, setProbes] = useState<Record<number, ProbeGroup[] | 'running'>>({})

  async function probe(id: number) {
    setProbes((p) => ({ ...p, [id]: 'running' }))
    try {
      // 逐把凭证探（口径层 v0.38），所以结果是按凭证分的组。
      const r = await api.post<{ credentials: ProbeGroup[] }>(`/channels/${id}/probe`)
      setProbes((p) => ({ ...p, [id]: r.credentials }))
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
        /* 关掉就重拉：凭证计数会变，「缺凭证」那个标记得跟着消失。 */
        <CredentialPool
          channel={credFor}
          onClose={() => {
            setCredFor(null)
            void reload()
          }}
        />
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
  probe?: ProbeGroup[] | 'running'
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const models = ch.models ?? []
  const protos = ch.protocols ?? []

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
          {/* 可用凭证归零是渠道从能用变不能用的唯一运行期路径（摘光不设特例，
              口径层 v0.38），而且启用渠道零凭证连启动闸都过不去，所以这条得显眼。
              有凭证时也把数目摆出来——3 把里坏了 2 把，只说「有凭证」等于把劣化
              过程整个藏住。 */}
          {ch.enabled_keys === 0 ? (
            <span className="tag tag-warn">缺凭证</span>
          ) : (
            <span className="tag" title="可用凭证数">
              凭证 {ch.enabled_keys}
            </span>
          )}
          {ch.disabled_keys > 0 && (
            <span className="tag tag-warn" title="401 自动摘除或人工停用，只能人工恢复">
              停用 {ch.disabled_keys}
            </span>
          )}
        </div>
        <div className="row-actions">
          <button className="btn btn-quiet" onClick={onProbe} disabled={probe === 'running'}>
            {probe === 'running' ? '探测中…' : '探测协议'}
          </button>
          <button className="btn btn-quiet" onClick={onCredential}>
            凭证池
          </button>
          <button className="btn btn-quiet" onClick={onEdit}>
            编辑
          </button>
          <Confirm ghost onConfirm={() => void mutate(() => api.del(`/channels/${ch.id}`))} />
        </div>
      </div>

      <div className="channel-url">{ch.base_url}</div>

      {/* 探测结论只提示，不挡任何操作，也不落库——它会过期（口径层 v0.33）。
          按凭证分行（v0.38）：同一个子路径对不同的号可以有不同结论，合成一条就把
          「哪一把不行」抹掉了。 */}
      {Array.isArray(probe) &&
        probe.map((g) => <ProbeRow key={g.credential} group={g} multi={probe.length > 1} />)}

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

/** ProbeRow 是一份凭证的探测结论。全通报一句，不通的逐条列。 */
function ProbeRow({ group, multi }: { group: ProbeGroup; multi: boolean }) {
  const unreachable = group.results.filter((r) => !r.reachable)
  const who = group.credential ? group.credential + (group.disabled ? '（已停用）' : '') : ''
  return (
    <div className={'probe' + (unreachable.length > 0 ? ' probe-bad' : '')}>
      {unreachable.length === 0 ? (
        <span>
          {multi && who ? `${who}：` : ''}探测通过：勾选的 {group.results.length} 个协议子路径上游都有
        </span>
      ) : (
        <>
          <span>
            {multi && who ? `${who}：` : ''}探测未通过 {unreachable.length}{' '}
            项——只是提示，不影响保存与路由，但这些协议的客户端打过来会 404：
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

const PROTOCOLS: Protocol[] = ['anthropic', 'openai', 'openai_responses']

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
  // 支持协议集（口径层 v0.33）。默认只勾 OpenAI：绝大多数上游只提供它，多勾一个探测
  // 不过反而要人回来改。
  const [protos, setProtos] = useState<Protocol[]>(
    channel?.protocols?.length ? channel.protocols : ['openai'],
  )
  const [baseURL, setBaseURL] = useState(channel?.base_url ?? '')
  const [keyMode, setKeyMode] = useState<KeyMode>(channel?.key_mode ?? 'polling')
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
      const body = { name, protocols: protos, base_url: baseURL, key_mode: keyMode, disabled }
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
            soon={PROTOCOL_SOON}
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
        {/* 露出选取模式（口径层 v0.38）：多凭证放开之后，「为什么总是第一把在跑」
            是必然被问的第一个问题，答案不该只藏在 SQL 里。 */}
        <Field
          label="凭证选取"
          hint="池子里有多把时按哪种顺序用。轮询把量摊开；随机适合上游按 key 限流、想避开固定节奏的场景"
        >
          <Segmented value={keyMode} options={KEY_MODE_OPTIONS} onChange={setKeyMode} />
        </Field>
        {!channel && (
          <Field label="上游凭证" hint="只写不回读：保存之后页面上再也看不到它。建完可以在「凭证池」里继续加">
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

/**
 * CredentialPool 是渠道凭证池的管理面（口径层 v0.38）。
 *
 * 逐条 CRUD，不是整把替换：值不回读 ⇒ 页面上没法把新贴进来的这堆与库里已有的对齐；
 * 而覆盖还会连带清掉已停用的凭证，那是 401 摘除的现场，是「这把为什么不转了」的唯一
 * 记录。列表里只有名字与状态——没有凭证值，也没有掩码。
 */
function CredentialPool({ channel, onClose }: { channel: Channel; onClose: () => void }) {
  const { data, error, reload, setError } = useList(() =>
    api.get<Credential[] | null>(`/channels/${channel.id}/credentials`),
  )
  const list = data ?? []

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

  return (
    <Dialog title={`凭证池：${channel.name}`} onClose={onClose}>
      <div className="form">
        <div className="bar bar-warn">
          上游凭证只写不回读，服务端不会把它发回来，掩码也不做。认凭证靠下面这个名字——
          它会出现在调用流水与用量里，渠道内不能重名。
        </div>
        <ErrorBar message={error} />

        {list.length === 0 ? (
          <Empty>这个渠道还没有凭证。启用中的渠道没有可用凭证会连启动都过不去。</Empty>
        ) : (
          <div className="cred-list">
            {list.map((c) => (
              <CredentialRow key={c.id} cred={c} mutate={mutate} />
            ))}
          </div>
        )}

        <AddCredentials channelID={channel.id} mutate={mutate} />

        <div className="form-actions">
          <button type="button" className="btn btn-primary" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </Dialog>
  )
}

/** CredentialRow 是池子里的一行：改名、停用/启用、删除。凭证值改不了也看不到。 */
function CredentialRow({
  cred,
  mutate,
}: {
  cred: Credential
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [name, setName] = useState(cred.name)

  return (
    <div className={'cred' + (cred.disabled ? ' is-off' : '')}>
      <input
        className="cred-name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={() => {
          if (name.trim() && name !== cred.name) {
            void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: cred.disabled }))
          }
        }}
      />
      <div className="cred-state">
        {cred.disabled ? (
          /* 摘除只人工恢复（口径层 v0.38），所以原因与时刻要一直摆着——它就是
             「这把为什么不转了」的唯一记录。 */
          <span className="tag tag-off" title={cred.disabled_at}>
            {cred.disabled_reason || '已停用'}
          </span>
        ) : (
          <span className="muted">{cred.created_at}</span>
        )}
      </div>
      <div className="row-actions">
        <Toggle
          on={!cred.disabled}
          onChange={(on) =>
            void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: !on }))
          }
        />
        <Confirm ghost onConfirm={() => void mutate(() => api.del(`/credentials/${cred.id}`))} />
      </div>
    </div>
  )
}

/**
 * AddCredentials 往池子里**追加**。
 *
 * 单条可以自己起名字；一次贴一批（一行一份）时名字由后端给 `凭证 N`——批量粘贴的
 * 场景里人手上只有一堆 key，逼他为每一行想个名字只会让这个入口没人用。
 */
function AddCredentials({
  channelID,
  mutate,
}: {
  channelID: number
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [credential, setCredential] = useState('')
  const [bulk, setBulk] = useState('')
  const [batch, setBatch] = useState(false)

  const lines = bulk
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  const ready = batch ? lines.length > 0 : credential.trim().length > 0

  return (
    <form
      className="form"
      onSubmit={(e) => {
        e.preventDefault()
        if (!ready) return
        const body = batch ? { credentials: bulk } : { name, credential }
        setName('')
        setCredential('')
        setBulk('')
        void mutate(() => api.post(`/channels/${channelID}/credentials`, body))
      }}
    >
      <Field
        label={batch ? '批量粘贴（一行一份，追加）' : '添加一份凭证'}
        hint={
          batch
            ? '只追加，不覆盖已有的——名字由网关给「凭证 N」，之后可以改'
            : '只追加，不影响池子里已有的几份；名字之后随时能改'
        }
      >
        {batch ? (
          <textarea rows={4} value={bulk} onChange={(e) => setBulk(e.target.value)} />
        ) : (
          /* 两行，key 在上：这一行是必填的那个，名字只是给它起个称呼，
             缺省时后端会给「凭证 N」。挤成一行会让两个宽度需求差很多的输入
             互相将就。 */
          <div className="cred-add">
            <input
              type="password"
              autoComplete="off"
              placeholder="上游 API key（必填）"
              value={credential}
              onChange={(e) => setCredential(e.target.value)}
            />
            <input
              placeholder="名字（可留空，默认「凭证 N」）"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
        )}
      </Field>
      <div className="form-actions">
        <button type="button" className="btn btn-quiet" onClick={() => setBatch(!batch)}>
          {batch ? '改为单条添加' : '批量粘贴'}
        </button>
        <button className="btn btn-primary" disabled={!ready}>
          {batch && lines.length > 1 ? `添加 ${lines.length} 份` : '添加'}
        </button>
      </div>
    </form>
  )
}
