import { useEffect, useRef, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type {
  Channel,
  ChannelModel,
  ChannelProbe,
  ModelListResult,
  Protocol,
} from '../../api'
import { Confirm, CopyCode, Toggle } from '../../ui'
import { Avatar, ChannelIcon, ModelIcon, vendorForModel } from '../../icons'
import { ChannelForm } from './form'
import { CredentialSection } from './credentials'
import { ModelPicker } from './picker'
import { ModelProbeGrid, ProbeRow } from './probe'

/**
 * ChannelDetail 是模型页主画布：**主语是纳管模型**（口径层 v0.75 / v0.76）。
 *
 * H1 永远是栏目名「模型」。渠道是 sunken 身份条，跟等宽模型行不是同一层。
 * 「检测 / 上游设置 / 上游凭证」收进条内文字按钮；后两颗点开在列表上方展开井，默认
 * 收起，检测则直接发请求，结论落在井外。
 * 获取模型列表与手动添加是页头组合按钮；启停在渠道名旁立刻生效。
 */
export function ChannelDetail({
  ch,
  probe,
  onProbe,
  fetched,
  onFetchModelsDone,
  onCredentialsChanged,
  onDelete,
  onSaved,
  mutate,
}: {
  ch: Channel
  probe?: ChannelProbe | 'running'
  onProbe: () => void
  /**
   * 上游拉到的模型列表（裁决 1A——保留 fetched state）。拉取本身已收进弹框，
   * 这一份是弹框 onResults 回吐回来、写进上层 state 的成果，供模型格子里的
   * listedOn/listComplete 建议位用。仍是只进内存、刷新即失（口径层 v0.40）。
   */
  fetched?: ModelListResult[]
  /** 弹框拉到模型列表后回吐结果，调用方写回自己的 fetched state。 */
  onFetchModelsDone: (results: ModelListResult[]) => void
  onCredentialsChanged: () => void
  onDelete: () => void
  onSaved: (id: number) => void
  /** 回 false 表示这次写没成——挑选面板据此决定关不关框，别的调用方不看。 */
  mutate: (fn: () => Promise<unknown>) => Promise<boolean>
}) {
  const [picking, setPicking] = useState(false)
  const [adding, setAdding] = useState(false)
  const [well, setWell] = useState<'settings' | 'creds' | null>(null)
  const models = ch.models ?? []
  const protos = ch.protocols ?? []
  const listed = Array.isArray(fetched) ? fetched : null

  // 上游在哪些协议侧列出了这个模型。**只用于给建议**，不自动改配置——拉回来的列表
  // 可能是中转站写死的（口径层 v0.40），采信它等于把探测做成了闸。
  function listedOn(model: string): Protocol[] {
    if (!listed) return []
    return listed
      .filter((r) => (r.models ?? []).includes(model))
      .flatMap((r) => r.protocols)
      .filter((p) => protos.includes(p))
  }
  // 渠道的每一个协议侧都真拉到了一份列表。**证据不全就不推断子集**：`models` 为 null
  // 是「这一侧没拉到」（401、超时、回的不是 JSON），与「拉到了但没列出它」在证据上是
  // 两回事，而 listedOn 把两者压成了同一个「不在里面」。按后者写库，等于凭零证据砍掉
  // 一条本来可能原生可走的协议路径，把请求推去做有损转换——比没推断坏得多。
  const listComplete =
    listed !== null &&
    protos.every((p) => listed.some((r) => r.models !== null && r.protocols.includes(p)))

  function toggleWell(next: 'settings' | 'creds') {
    setWell((cur) => (cur === next ? null : next))
  }

  return (
    <>
      <header className="page-head">
        <h1>模型</h1>
        <div className="split-act">
          <button
            type="button"
            className="split-act-main"
            onClick={() => setPicking(true)}
            title="拉上游 /v1/models 并挑选，只用来帮你填表，不落库也不影响路由"
          >
            <IconRefresh />
            获取模型列表
          </button>
          <button
            type="button"
            className={'split-act-plus' + (adding ? ' is-on' : '')}
            aria-label="手动添加模型"
            aria-pressed={adding}
            onClick={() => setAdding((v) => !v)}
          >
            <IconPlus />
          </button>
        </div>
      </header>

      <div className="ch-bar">
        <div className="ch-bar-kicker">渠道</div>
        <div className="ch-bar-row">
          <div className={'ch-id' + (ch.disabled ? ' is-off' : '')}>
            <ChannelIcon channel={ch} size={22} />
            <p className="ch-bar-name">{ch.name}</p>
            {protos.map((p) => (
              <span key={p} className="tag" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
                {PROTOCOL_SHORT[p] ?? p}
              </span>
            ))}
            {protos.length === 0 && <span className="tag tag-warn">协议集为空</span>}
            <Toggle
              on={!ch.disabled}
              label={ch.name}
              onChange={(on) =>
                void mutate(() =>
                  api.put(`/channels/${ch.id}`, {
                    name: ch.name,
                    protocols: protos,
                    base_url: ch.base_url,
                    disabled: !on,
                  }),
                )
              }
            />
          </div>
          <div className="ch-acts">
            {/* 检测的对象是渠道整体（协议子路径 + 凭证 + 模型），跟另两颗同属渠道操作层，
                所以坐在条上、不再藏进凭证井——它的结论本来就有一半（模型矩阵）在井外面。 */}
            <button
              type="button"
              className="btn btn-text"
              onClick={onProbe}
              disabled={probe === 'running'}
              title="给勾选的协议子路径各发一个最小请求；只提示，不落库也不影响路由"
            >
              {probe === 'running' ? '检测中…' : '检测'}
            </button>
            <button
              type="button"
              className={'btn btn-text well-toggle' + (well === 'settings' ? ' is-on' : '')}
              onClick={() => toggleWell('settings')}
            >
              上游设置
            </button>
            <button
              type="button"
              className={'btn btn-text well-toggle' + (well === 'creds' ? ' is-on' : '')}
              onClick={() => toggleWell('creds')}
            >
              上游凭证
            </button>
          </div>
        </div>
      </div>

      {ch.enabled_keys === 0 && (
        <div className="bar bar-warn">
          这个渠道没有可用凭证，所有走它的请求都会失败；启用状态下连启动闸都过不去。在「上游凭证」里加一份。
        </div>
      )}

      {well === 'settings' && (
        <div className="well">
          {/* 不要再挂 key={ch.id}：调用方已经在 ChannelDetail 上挂了。 */}
          <ChannelForm channel={ch} onSaved={onSaved} />
          <div className="well-foot">
            <Confirm ghost label="删除渠道" onConfirm={onDelete} />
          </div>
        </div>
      )}

      {well === 'creds' && (
        <div className="well">
          <CredentialSection channel={ch} onChanged={onCredentialsChanged} />
        </div>
      )}

      {/* 探测结论跟着按钮出井：按钮在条上、结果锁在折叠井里是断的。放在井之后、模型
          列表之前——井默认收起，于是它平时就紧跟在渠道条下面，且始终与模型矩阵相邻。 */}
      {probe && probe !== 'running' && (
        <div className="detail-probes">
          {probe.credentials.map((g) => (
            <ProbeRow key={g.credential} group={g} multi={probe.credentials.length > 1} />
          ))}
        </div>
      )}
      {probe && probe !== 'running' && (probe.models?.length ?? 0) > 0 && (
        <ModelProbeGrid rows={probe.models!} credential={probe.model_credential} />
      )}
      {picking && (
        <ModelPicker
          channel={ch}
          initial={listed ?? undefined}
          existing={new Set(models.map((m) => m.upstream_model))}
          onClose={() => setPicking(false)}
          onResults={(r) => onFetchModelsDone(r)}
          onAdd={async (names) => {
            const ok = await mutate(async () => {
              for (const name of names) {
                const on = listedOn(name)
                await api.post(`/channels/${ch.id}/models`, {
                  upstream_model: name,
                  protocols: listComplete && on.length < protos.length ? on : [],
                })
              }
            })
            if (ok) setPicking(false)
          }}
        />
      )}

      {adding && (
        <AddModels
          channel={ch}
          mutate={mutate}
          onClose={() => setAdding(false)}
        />
      )}

      <div className="models">
        <div className="models-title">纳管模型 · {models.length}</div>
        {models.length === 0 ? (
          <div className="muted models-empty">还没有纳管模型。填上游那边真实的模型名，比如 gpt-4o、deepseek-chat。</div>
        ) : (
          <div className="model-grid">
            {models.map((m) => (
              <div key={m.id} className={'model' + (m.disabled ? ' is-off' : '')}>
                <div className="model-id">
                  <ModelIcon model={m.upstream_model} size={22} />
                  <div className="model-id-text">
                  <code className="model-name" title={m.upstream_model}>
                    {m.upstream_model}
                  </code>
                  {m.disabled ? (
                    <span className="model-q muted">
                      {ch.name}/{m.upstream_model}
                    </span>
                  ) : (
                    <CopyCode
                      className="model-q"
                      value={`${ch.name}/${m.upstream_model}`}
                      title={`点击复制 ${ch.name}/${m.upstream_model}`}
                    />
                  )}
                  </div>
                </div>
                <div className="model-actions">
                  <Toggle
                    on={!m.disabled}
                    label={m.upstream_model}
                    onChange={(on) =>
                      void mutate(() => api.put(`/channel-models/${m.id}`, { disabled: !on }))
                    }
                  />
                  <Confirm
                    ghost
                    onConfirm={() => void mutate(() => api.del(`/channel-models/${m.id}`))}
                  />
                </div>
                {(protos.length > 1 || (m.protocols ?? []).length > 0) && (
                  <ModelProtocols
                    model={m}
                    channelProtocols={protos}
                    listedOn={listedOn(m.upstream_model)}
                    listComplete={listComplete}
                    mutate={mutate}
                  />
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  )
}

/**
 * ModelProtocols 是模型格子里那行协议子集（口径层 v0.40）。
 *
 * 只在渠道支持多个协议时出现——单协议渠道没有子集可言，摆一行只能勾一个的 chips
 * 是纯噪音。它总是占一行而不是「有值才出现」：模型网格里某一格凭空高一截，同一行
 * 其它格会跟着拉高，整片网格参差（同 .model-name 那条截断不换行的理由）。
 *
 * 全勾等价于继承，所以勾满时归一成空数组存回去，不在库里留一份跟渠道集重复的冗余：
 * 那份冗余会在渠道日后加一个协议时，悄悄把新协议挡在这个模型外面。
 */
function ModelProtocols({
  model,
  channelProtocols,
  listedOn,
  listComplete,
  mutate,
}: {
  model: ChannelModel
  channelProtocols: Protocol[]
  /** 上游在哪些协议侧列出了这个模型。空数组 = 没拉过，或哪一侧都没列。 */
  listedOn: Protocol[]
  /** 渠道的每一侧都真拉到了列表。为假时 listedOn 的空缺分不清「没列出」和「没拉到」。 */
  listComplete: boolean
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const current = model.protocols ?? []
  const inherit = current.length === 0
  // 渠道协议集缩小之后，模型上没跟着改的那些值会留在这儿（宽松存，见口径层 v0.40）。
  // 照实显示而不是悄悄滤掉：它们此刻确实让这个模型不可用，藏起来只会让人对着一个
  // 「看上去哪都没问题」的配置查 503。
  const stale = current.filter((p) => !channelProtocols.includes(p))

  function save(next: Protocol[]) {
    const inChannel = channelProtocols.filter((p) => next.includes(p))
    const rest = next.filter((p) => !channelProtocols.includes(p))
    // 勾满且没有失效项才归一成继承——还留着失效项时归零会把它们一并抹掉，
    // 而那是人没点过的东西。
    const norm = inChannel.length === channelProtocols.length && rest.length === 0 ? [] : [...inChannel, ...rest]
    void mutate(() =>
      api.put(`/channel-models/${model.id}`, { disabled: model.disabled, protocols: norm }),
    )
  }

  function toggle(p: Protocol) {
    save(current.includes(p) ? current.filter((x) => x !== p) : [...current, p])
  }

  // 建议只在「上游确实只列出了一部分」时给，且不自动应用——拉回来的列表可能是中转站
  // 写死的，采信它等于把探测做成了闸（口径层 v0.33 立论）。
  const suggest =
    listComplete && listedOn.length > 0 && listedOn.length < channelProtocols.length
      ? listedOn
      : null
  const same =
    suggest !== null &&
    suggest.length === current.length &&
    suggest.every((p) => current.includes(p))

  return (
    <div className="model-protocols">
      <span className="model-protocols-label" title="不勾 = 跟渠道一样。勾了就只走勾中的那些。">
        协议
      </span>
      {channelProtocols.map((p) => (
        <button
          key={p}
          type="button"
          className={'chip-toggle' + (current.includes(p) ? ' is-on' : '')}
          onClick={() => toggle(p)}
          title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}
        >
          {PROTOCOL_SHORT[p] ?? p}
        </button>
      ))}
      {stale.map((p) => (
        <button
          key={p}
          type="button"
          className="chip-toggle is-stale"
          onClick={() => toggle(p)}
          title={`渠道已经不说 ${PROTOCOL_LABEL[p] ?? p} 了，这一项正让这个模型没有可用协议。点一下移除。`}
        >
          {PROTOCOL_SHORT[p] ?? p}
        </button>
      ))}
      {inherit && stale.length === 0 && <span className="muted">跟渠道一样</span>}
      {!inherit && stale.length === current.length && (
        <span className="tag tag-warn" title="与渠道协议集没有交集，这个模型当下用不了">
          无可用协议
        </span>
      )}
      {suggest && !same && (
        <button type="button" className="chip-toggle chip-suggest" onClick={() => save(suggest)}>
          上游只在 {suggest.map((p) => PROTOCOL_SHORT[p] ?? p).join('、')} 侧列出 · 采纳
        </button>
      )}
      {listComplete && listedOn.length === 0 && (
        <span className="muted" title="拉回来的列表里没有这个名字。可能是上游没提供 /v1/models，也可能是名字写错了">
          上游列表里没有它
        </span>
      )}
    </div>
  )
}

function IconRefresh() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path
        d="M13.2 8a5.2 5.2 0 1 1-1.5-3.6"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      />
      <path
        d="M13.2 2.8v2.8h-2.8"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function IconPlus() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M8 3.2v9.6M3.2 8h9.6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
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
  onClose,
}: {
  channel: Channel
  mutate: (fn: () => Promise<unknown>) => Promise<boolean>
  onClose: () => void
}) {
  const [draft, setDraft] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const existing = new Set((channel.models ?? []).map((m) => m.upstream_model))

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

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
        void mutate(async () => {
          // 串行而不是 Promise.all：SQLite 那头连接池是 1，并发写只会排队，
          // 而串行出错时能停在第一个失败上，不至于半成功一片。
          for (const m of fresh) {
            await api.post(`/channels/${channel.id}/models`, { upstream_model: m })
          }
        }).then((ok) => {
          if (!ok) return
          setDraft('')
          onClose()
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
          ref={inputRef}
          placeholder="上游模型名，可一次粘一批（逗号或换行分隔）"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && draft.trim() === '') {
              e.preventDefault()
              onClose()
            }
          }}
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
