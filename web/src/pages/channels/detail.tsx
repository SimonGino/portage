import { useEffect, useRef, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_ORDER, PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type {
  BaseURLs,
  Channel,
  ChannelModel,
  ModelListResult,
  Protocol,
} from '../../api'
import { Confirm, CopyCode, DetailBlock, Dialog, Toggle } from '../../ui'
import { Avatar, ChannelIcon, ModelIcon, vendorForModel } from '../../icons'
import { ChannelForm, joinURL } from './form'
import { CredentialBlock } from './credentials'
import { ModelPicker } from './picker'

/**
 * ChannelDetail 是模型页主画布：**主语是纳管模型**（口径层 v0.75 / v0.76）。
 *
 * H1 永远是栏目名「模型」。渠道是 sunken 身份条，跟等宽模型行不是同一层。
 * 「API 地址」「上游凭证」是身份条下**默认收起的区块**（PO 2026-08-20 裁决提出井外，
 * 2026-08-24 裁决地址在上——地址定义这个渠道是谁、声明了哪些协议，凭证是从属物，
 * 接一家上游也是先填地址再贴 key；2026-08-26 裁决默认收起成一行摘要、点开原地展开
 * ——这页第一眼该是纳管模型，两块常驻会把模型列表推到一屏以下）。
 * 「上游设置」（改名、并发、能力位、删除）是条内文字按钮点开的**弹框**（PO
 * 2026-08-24 裁决，推翻 v0.75 的展开井：与管理、检测、挑选同一形制，页面上不再有
 * 顶开内容的井，也不再需要「收起后未保存」那套小圆点机制）。检测在凭证行里，点开
 * 检测弹层（口径层 v0.96 ③）：发起、勾选、结果都在弹层里，关弹层即失——页面上
 * 不再有常驻探测区块。获取模型列表与手动添加是页头组合按钮；启停在渠道名旁立刻生效。
 */
export function ChannelDetail({
  ch,
  fetched,
  onFetchModelsDone,
  onCredentialsChanged,
  onDelete,
  onSaved,
  mutate,
}: {
  ch: Channel
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
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 设置表单有没有未保存的改动——只喂给 Dialog 的 guard：改到一半时遮罩误点不关框。
  // Esc 仍照 Dialog 的通则丢弃，弹框关了编辑就没了，不再有「收起还留着」的中间态。
  const [settingsDirty, setSettingsDirty] = useState(false)
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
              onChange={(on) => void mutate(() => api.put(`/channels/${ch.id}/disabled`, { disabled: !on }))}
            />
          </div>
          <div className="ch-acts">
            {/* 检测挪去了凭证行（它验的首先是 key），凭证与地址是身份条下的区块——
                条上只剩「上游设置」这一个弹框入口。 */}
            <button type="button" className="btn btn-link" onClick={() => setSettingsOpen(true)}>
              上游设置
            </button>
          </div>
        </div>
      </div>

      {ch.enabled_keys === 0 && (
        <div className="bar bar-warn">
          这个渠道没有可用凭证，所有走它的请求都会失败；启用状态下连启动闸都过不去。在下面「上游凭证」贴一份即可。
        </div>
      )}

      {settingsOpen && (
        <Dialog
          title="上游设置"
          guard={settingsDirty}
          onClose={() => {
            setSettingsOpen(false)
            setSettingsDirty(false)
          }}
        >
          {/* 不要再挂 key={ch.id}：调用方已经在 ChannelDetail 上挂了。 */}
          <ChannelForm
            channel={ch}
            onSaved={(id) => {
              setSettingsOpen(false)
              setSettingsDirty(false)
              onSaved(id)
            }}
            onDirtyChange={setSettingsDirty}
            onDelete={onDelete}
          />
        </Dialog>
      )}

      <BaseURLBlock ch={ch} mutate={mutate} />

      <CredentialBlock channel={ch} onChanged={onCredentialsChanged} />

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
 * BaseURLBlock 是 API 地址在模型页上的区块（PO 2026-08-20 裁决与凭证区块一起
 * 从「上游设置」井里提出来——它们是接一家上游要填的全部，不该藏两层；
 * 2026-08-24 裁决它排在凭证前面：地址定义渠道是谁，凭证是从属物；2026-08-26
 * 裁决默认收起，收起行摘要是已声明的协议——这页第一眼该是纳管模型）。
 *
 * v0.96 起**每协议一行**：协议名 + 地址，失焦或回车即存，预览逐行紧随其下；
 * 「添加端点」加行，**删行 = 取消声明该协议**（不加确认——恢复就是再填一次地址），
 * 至少保留一行，删到最后一行的口被服务端和这里一起堵住。
 * 写走 base-url 那一笔意图写（#48 批2），别的渠道字段碰不到。
 */
function BaseURLBlock({
  ch,
  mutate,
}: {
  ch: Channel
  mutate: (fn: () => Promise<unknown>) => Promise<boolean>
}) {
  const [urls, setUrls] = useState<BaseURLs>({ ...ch.base_url })
  // 「添加端点」刚加出来、还没填值的空行：值为空不算声明，不在 ch.base_url 里，
  // 单独记着才不会一失焦就消失。
  const [blank, setBlank] = useState<Protocol[]>([])
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (!saved) return
    const t = setTimeout(() => setSaved(false), 2000)
    return () => clearTimeout(t)
  }, [saved])

  function put(next: BaseURLs) {
    return mutate(() => api.put(`/channels/${ch.id}/base-url`, { base_url: next }))
  }

  function save(p: Protocol) {
    const next = (urls[p] ?? '').trim()
    const prev = ch.base_url[p] ?? ''
    if (next === prev) return
    // 清空后失焦不当「取消声明」提交：取消声明只走「删行」那一个口（口径层 v0.96 ②），
    // 清空多半是要重填。已声明的行清空就退回库里的值，别把一次犹豫变成一次删除。
    if (next === '' && prev !== '') {
      setUrls((u) => ({ ...u, [p]: prev }))
      return
    }
    if (next === '') return
    void put({ ...urls, [p]: next }).then((ok) => {
      if (ok) setSaved(true)
    })
  }

  function remove(p: Protocol) {
    if (blank.includes(p)) {
      setBlank((prev) => prev.filter((x) => x !== p))
      setUrls((prev) => {
        const map = { ...prev }
        delete map[p]
        return map
      })
      return
    }
    const map = { ...urls }
    delete map[p]
    void put(map).then((ok) => {
      if (!ok) return
      setUrls(map)
      setSaved(true)
    })
  }

  // 行 = 已声明的协议 + 刚加出来的空行，按固定回退序排，别跟着敲键盘的顺序跳。
  const rows = PROTOCOL_ORDER.filter(
    (p) => (ch.base_url[p] ?? '') !== '' || blank.includes(p) || (urls[p] ?? '').trim() !== '',
  )
  const addable = PROTOCOL_ORDER.filter((p) => !rows.includes(p))
  // 收起行的摘要是已声明的协议——地址本身太长，而「声明了哪些协议」正是这块
  // 定义渠道身份的那一半。
  const declared = PROTOCOL_ORDER.filter((p) => (ch.base_url[p] ?? '') !== '')

  return (
    <DetailBlock
      title="API 地址"
      summary={declared.map((p) => PROTOCOL_SHORT[p] ?? p).join(' + ')}
    >
      {rows.map((p) => {
        const v = urls[p] ?? ''
        const dirty = v.trim() !== (ch.base_url[p] ?? '')
        return (
          /* 定宽协议名列 + 输入列：OpenAI 与 Anthropic 词长不同，不定宽的话
             两行输入框左边参差。预览是输入列下的一行小字，不再套底色盒——
             盒装预览是表单井里「一段汇总」的形制，逐行紧随时它比输入框还高。 */
          <div key={p} className="baseurl-proto-row">
            <span className="baseurl-proto" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
              {PROTOCOL_SHORT[p] ?? p}
            </span>
            <div className="baseurl-main">
              <div className="baseurl-row">
                <input
                  className="baseurl-input"
                  value={v}
                  onChange={(e) => setUrls((prev) => ({ ...prev, [p]: e.target.value }))}
                  onBlur={() => save(p)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      save(p)
                    }
                  }}
                  placeholder="https://…（协议子路径之前的前缀，网关自己接子路径）"
                />
                {/* 失焦即存没有按钮可按，回执只能是一句字：改着（还没存）与刚存上各说各的。 */}
                {dirty ? (
                  <span className="muted baseurl-state">回车保存</span>
                ) : saved ? (
                  <span className="muted baseurl-state">已保存</span>
                ) : null}
                {/* 删行 = 取消声明这个协议，不加确认（口径层 v0.96 ②：恢复就是再填一次）。
                    只剩一行时不给口——渠道至少要声明一个协议，服务端也会拒。 */}
                {rows.length > 1 && (
                  <button
                    type="button"
                    className="btn btn-link baseurl-del"
                    title={`删除这一行 = 这个渠道不再声明 ${PROTOCOL_LABEL[p]}`}
                    onClick={() => remove(p)}
                  >
                    删行
                  </button>
                )}
              </div>
              {v.trim() !== '' && (
                <div
                  className="baseurl-preview"
                  title="网关实际会请求的完整地址：你填的前缀 + 协议固定子路径"
                >
                  → <code>{joinURL(v, PROTOCOL_PATH[p] ?? '')}</code>
                </div>
              )}
            </div>
          </div>
        )
      })}
      {addable.length > 0 && (
        <div className="baseurl-add">
          {addable.map((p) => (
            <button
              key={p}
              type="button"
              className="chip-toggle"
              title={`给 ${PROTOCOL_LABEL[p]} 加一行地址，填了即声明该协议`}
              onClick={() => setBlank((prev) => [...prev, p])}
            >
              + {PROTOCOL_SHORT[p] ?? p}
            </button>
          ))}
        </div>
      )}
    </DetailBlock>
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
