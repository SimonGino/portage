import { useEffect, useRef, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_ORDER, PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type {
  BaseURLs,
  Channel,
  ChannelModel,
  ModelListResult,
  PricingModelPrice,
  PricingModels,
  Protocol,
} from '../../api'
import { Confirm, CopyCode, CopyIconButton, DetailBlock, Dialog, Toggle } from '../../ui'
import { Avatar, ChannelIcon, ModelIcon, vendorForModel } from '../../icons'
import { IconCheck, IconPencil, IconSliders, IconX } from '../../icons/acts'
import { ChannelForm, joinURL } from './form'
import { CredentialBlock } from './credentials'
import { ModelPicker } from './picker'

/**
 * ChannelDetail 是模型页主画布：**主语是纳管模型**（口径层 v0.75 / v0.76）。
 *
 * H1 永远是栏目名「模型」。渠道是 sunken 身份条，跟等宽模型行不是同一层。
 * 「API 地址」「上游凭证」是身份条下**常驻展开的区块**（PO 2026-08-20 裁决提出井外，
 * 2026-08-24 裁决地址在上——地址定义这个渠道是谁、声明了哪些协议，凭证是从属物，
 * 接一家上游也是先填地址再贴 key；2026-08-28 裁决回到常驻展开，推翻 08-26 的默认
 * 收起——管理、检测、每协议的预览地址都要一眼可见，折一层就是多一步）。
 * 「上游设置」（改名、并发、能力位、删除）是条内文字按钮点开的**弹框**（PO
 * 2026-08-24 裁决，推翻 v0.75 的展开井：与管理、检测、挑选同一形制，页面上不再有
 * 顶开内容的井，也不再需要「收起后未保存」那套小圆点机制）。检测在凭证行里，点开
 * 检测弹层（口径层 v0.96 ③）：发起、勾选、结果都在弹层里，关弹层即失——页面上
 * 不再有常驻探测区块。获取模型列表与手动添加落在「纳管模型」标题旁；启停在渠道名旁立刻生效。
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
  // models.dev 的建议价（口径层 §2.10，#74）：渠道标注了 provider 才拉，一渠道一发。
  // 只做填表助手——建议不落库、不参与计价，人点「采纳」写进去的才算数。拉失败就当
  // 没有建议（快照是发版内置资产，失败多半是版本不齐），不为它挂错误条。
  const [suggested, setSuggested] = useState<Record<string, PricingModelPrice> | null>(null)
  useEffect(() => {
    setSuggested(null)
    if (!ch.provider) return
    let gone = false
    api
      .get<PricingModels>(`/pricing/models?provider=${encodeURIComponent(ch.provider)}`)
      .then((r) => {
        if (!gone) setSuggested(r.models)
      })
      .catch(() => {})
    return () => {
      gone = true
    }
  }, [ch.provider])
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
            <button type="button" className="act" onClick={() => setSettingsOpen(true)}>
              <IconSliders />
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

      <DetailBlock
        title={`纳管模型 · ${models.length}`}
        action={
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
        }
      >
        {adding && (
          <AddModels
            channel={ch}
            mutate={mutate}
            onClose={() => setAdding(false)}
          />
        )}
        {models.length === 0 ? (
          <div className="muted models-empty">还没有纳管模型。点「获取模型列表」，或手填上游那边真实的模型名。</div>
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
                {/* 上限与定价并排一行（PO 2026-08-30：「合并成一行不好吗，减少高度」）：
                    两颗收起态都是芯片，各占一整行白耗一倍高度。各自仍是独立组件
                    （编辑态原地展开），容器只负责排成一行，放不下时折行。 */}
                <div className="model-meta">
                  <ModelInputLimit model={m} mutate={mutate} />
                  <ModelPrices
                    model={m}
                    suggest={suggested?.[m.upstream_model] ?? null}
                    mutate={mutate}
                  />
                </div>
              </div>
            ))}
          </div>
        )}
      </DetailBlock>
    </>
  )
}

/**
 * BaseURLBlock 是 API 地址在模型页上的区块（PO 2026-08-20 裁决与凭证区块一起
 * 从「上游设置」井里提出来——它们是接一家上游要填的全部，不该藏两层；
 * 2026-08-24 裁决它排在凭证前面：地址定义渠道是谁，凭证是从属物；2026-08-28
 * 裁决常驻展开、默认只读——每协议一行**预览地址**（网关实际会请求的完整地址）
 * 一眼可见，输入框、删行、加行收进「编辑」原地切换：日常进这页是核对地址，
 * 不是改地址，常驻一排输入框全是待办的样子）。
 *
 * 编辑态仍是 v0.96 的**每协议一行**：协议名 + 地址，失焦或回车即存，预览逐行
 * 紧随其下；「添加端点」加行，**删行 = 取消声明该协议**（不加确认——恢复就是
 * 再填一次地址），至少保留一行，删到最后一行的口被服务端和这里一起堵住。
 * 「完成」只收面板不提交——每一笔都已在失焦时写掉。
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
  const [editing, setEditing] = useState(false)
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
  const declared = PROTOCOL_ORDER.filter((p) => (ch.base_url[p] ?? '') !== '')

  if (!editing) {
    return (
      <DetailBlock
        title="API 地址"
        action={
          <button type="button" className="act" onClick={() => setEditing(true)}>
            <IconPencil />
            编辑
          </button>
        }
      >
        {declared.map((p) => {
          const url = joinURL(ch.base_url[p] ?? '', PROTOCOL_PATH[p] ?? '')
          return (
            <div key={p} className="baseurl-view-row">
              <span className="baseurl-proto" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
                {PROTOCOL_SHORT[p] ?? p}
              </span>
              <code
                className="baseurl-view-url"
                title="网关实际会请求的完整地址：你填的前缀 + 协议固定子路径"
              >
                {url}
              </code>
              {/* 值旁微钮（DESIGN v0.38 ② 成例，凭证行同款）：这串地址正是要抄进
                  客户端配置的东西，手抄一个长域名必错，复制是它的常用动作。 */}
              <CopyIconButton value={url} title="复制完整地址" />
            </div>
          )
        })}
      </DetailBlock>
    )
  }

  return (
    <DetailBlock
      title="API 地址"
      action={
        <button
          type="button"
          className="act"
          onClick={() => {
            // 「完成」只收面板：每一笔都在失焦时已写掉，这里只把没填值的空行清掉。
            setBlank([])
            setEditing(false)
          }}
        >
          <IconCheck />
          完成
        </button>
      }
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
                    className="act-icon baseurl-del"
                    aria-label="删行"
                    title={`删除这一行 = 这个渠道不再声明 ${PROTOCOL_LABEL[p]}`}
                    onClick={() => remove(p)}
                  >
                    <IconX />
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

/** 输入上限显示成 200k 这种紧凑形；不整千的照原样带分隔符摆。 */
function fmtTokens(n: number): string {
  return n >= 1000 && n % 1000 === 0 ? `${n / 1000}k` : n.toLocaleString('en-US')
}

/** 解析上限输入：裸数字，或 `200k` / `1m` 这种紧凑写法（显示用的正是这种形，
 *  输入也就该认它）。解析不出回 null。 */
function parseTokens(raw: string): number | null {
  const m = /^(\d+)([km]?)$/i.exec(raw.trim())
  if (!m) return null
  const n = Number(m[1]) * (m[2].toLowerCase() === 'k' ? 1000 : m[2].toLowerCase() === 'm' ? 1000000 : 1)
  return Number.isFinite(n) ? n : null
}

/**
 * ModelInputLimit 是模型行上的「输入上限（估算）」（口径层 v0.99；DESIGN v0.38
 * 收进芯片家族，推翻 v0.36 的文字动作）。与协议芯片同行同族：未设 = 虚线空位芯片
 * 「+ 上限」，设了 = 实底芯片「上限 ~N」（悬停浮出铅笔说「能改」）；点开是胶囊
 * 输入组（数字 + 单位一体），失焦或回车即存，空/0 = 清成不限，认 `200k`/`1m`
 * 紧凑写法。文案一律带「估算」——判据是请求体字节数 ÷4，不是真分词。
 */
function ModelInputLimit({
  model,
  mutate,
}: {
  model: ChannelModel
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const [editing, setEditing] = useState(false)
  const [val, setVal] = useState('')
  const limit = model.max_input_tokens ?? 0

  function save() {
    setEditing(false)
    const raw = val.trim()
    const n = raw === '' ? 0 : parseTokens(raw)
    if (n === null || n < 0) return
    if (n === limit) return
    void mutate(() =>
      api.put(`/channel-models/${model.id}`, { disabled: model.disabled, max_input_tokens: n }),
    )
  }

  if (editing) {
    return (
      <div className="model-protocols">
        <span className="model-protocols-label" title="估算输入 token 超过它就按 413 拒。按请求体字节数 ÷4 估算，不精确；含图片的请求会被高估。">
          上限
        </span>
        <span className="limit-edit">
          <input
            autoFocus
            value={val}
            inputMode="numeric"
            onChange={(e) => setVal(e.target.value.replace(/[^0-9kKmM]/g, ''))}
            onBlur={save}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                save()
              } else if (e.key === 'Escape') {
                setVal(String(limit || ''))
                setEditing(false)
              }
            }}
            placeholder="200k"
          />
          <span className="limit-edit-unit">token</span>
        </span>
        <span className="muted">估算 · 空 = 不限</span>
      </div>
    )
  }

  if (limit === 0) {
    return (
      <div className="model-protocols">
        <button
          type="button"
          className="chip-add"
          onClick={() => {
            setVal('')
            setEditing(true)
          }}
          title="设置输入上限（估算）：估算输入超过它按 413 拒。按请求体字节数 ÷4 估算，不精确"
        >
          + 上限
        </button>
      </div>
    )
  }

  return (
    <div className="model-protocols">
      <button
        type="button"
        className="model-limit-chip"
        onClick={() => {
          setVal(String(limit))
          setEditing(true)
        }}
        title={`输入上限（估算）${limit.toLocaleString('en-US')} token：估算输入超过它按 413 拒。按请求体字节数 ÷4 估算，不精确。点击修改`}
      >
        上限 ~{fmtTokens(limit)}
        <IconPencil />
      </button>
    </div>
  )
}

/** 单价显示：USD/百万 token 的定价惯用形（$3、$0.3、$3.75），不是金额展示的
 *  `$X.XX`——那条管的是算出来的钱，单价抹成两位会把 $0.075 写成 $0.08。 */
function fmtPrice(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  return '$' + String(n)
}

/** 四价各自的短标签，编辑胶囊与 title 共用一份，别两处各抄一遍。 */
const PRICE_FIELDS = [
  ['input', '入'],
  ['output', '出'],
  ['cache_read', '缓读'],
  ['cache_write', '缓写'],
] as const

/**
 * ModelPrices 是模型行上的「定价」（口径层 §2.10，#74；DESIGN v0.41 收进 v0.38
 * 那副胶囊家族）。与上限同层同形，三态同一副身形：未定价 = 虚线胶囊「+ 定价」，
 * 定了 = 实底芯片「$入/$出」悬停浮出铅笔（四价全文在 title）；编辑是胶囊输入组
 * ——四价各一个（数字与「$/M」单位同框），焦点离开整组或回车即存，Esc 丢弃。
 * **空 = 清回未定价（null），0 = 真免费**，两态别抹成一个。
 *
 * 「未定价」提醒的判据是**四价全 null 且有用量**：没人用过的条目不催着定价，
 * 用过的未定价条目每一笔 cost 都在记 0，钱正在悄悄漏。
 *
 * 建议价来自内置 models.dev 快照（渠道标注了 provider 才有），chip-suggest 同
 * 协议子集那颗「采纳」的形制：只提示，点了才落库；快照缺哪一价就建议 null，不补 0。
 */
function ModelPrices({
  model,
  suggest,
  mutate,
}: {
  model: ChannelModel
  /** models.dev 快照里这个模型的建议价。null = 没建议（没标注 provider / 快照里没有它）。 */
  suggest: PricingModelPrice | null
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<Record<string, string>>({})

  const current: Record<string, number | null> = {
    input: model.price_input,
    output: model.price_output,
    cache_read: model.price_cache_read,
    cache_write: model.price_cache_write,
  }
  const unpriced = PRICE_FIELDS.every(([k]) => current[k] === null)

  function put(prices: Record<string, number | null>) {
    void mutate(() => api.put(`/channel-models/${model.id}`, { disabled: model.disabled, prices }))
  }

  function save() {
    setEditing(false)
    const next: Record<string, number | null> = {}
    for (const [k] of PRICE_FIELDS) {
      const raw = (draft[k] ?? '').trim()
      if (raw === '') {
        next[k] = null
        continue
      }
      const n = Number(raw)
      // 解析不出或负数整组不存（同上限那颗的处置）：四价是一笔整组覆盖，
      // 存下能解析的那几个会把没看清的输入悄悄写成 null。
      if (!Number.isFinite(n) || n < 0) return
      next[k] = n
    }
    if (PRICE_FIELDS.every(([k]) => next[k] === current[k])) return
    put(next)
  }

  function open() {
    setDraft(
      Object.fromEntries(PRICE_FIELDS.map(([k]) => [k, current[k] === null ? '' : String(current[k])])),
    )
    setEditing(true)
  }

  const title = PRICE_FIELDS.map(([k, label]) => `${label} ${fmtPrice(current[k])}`).join('，')

  // 建议与现值逐项相等就不摆「采纳」：快照缺的价按 null 比，别拿 0 充数。
  const suggestDiffers =
    suggest !== null && PRICE_FIELDS.some(([k]) => (suggest[k] ?? null) !== current[k])
  const suggestChip = suggestDiffers && (
    <button
      type="button"
      className="chip-toggle chip-suggest"
      title={`models.dev 快照的建议价（USD/百万 token）：${PRICE_FIELDS.map(
        ([k, label]) => `${label} ${fmtPrice(suggest![k])}`,
      ).join('，')}。只是建议，点「采纳」才落库`}
      onClick={() =>
        put(Object.fromEntries(PRICE_FIELDS.map(([k]) => [k, suggest![k] ?? null])))
      }
    >
      models.dev {fmtPrice(suggest!.input)}/{fmtPrice(suggest!.output)} · 采纳
    </button>
  )

  if (editing) {
    return (
      <div className="model-protocols">
        <span
          className="model-protocols-label"
          title="USD/百万 token 的四项单价。留空 = 未定价（有用量记 0 并提醒），0 = 真免费。改价只影响之后的流水，不追溯"
        >
          定价
        </span>
        <span
          className="price-edit-group"
          onBlur={(e) => {
            // 四个输入框共用一次保存：焦点还在组内（在往下一格挪）不算离开。
            if (e.relatedTarget instanceof Node && e.currentTarget.contains(e.relatedTarget)) return
            save()
          }}
        >
          {PRICE_FIELDS.map(([k, label], i) => (
            <span key={k} className="limit-edit price-edit">
              <span className="price-edit-label">{label}</span>
              <input
                autoFocus={i === 0}
                value={draft[k] ?? ''}
                inputMode="decimal"
                onChange={(e) => setDraft((d) => ({ ...d, [k]: e.target.value.replace(/[^0-9.]/g, '') }))}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    save()
                  } else if (e.key === 'Escape') {
                    setEditing(false)
                  }
                }}
                placeholder="—"
              />
            </span>
          ))}
          <span className="limit-edit-unit price-edit-unit">$/M</span>
        </span>
        <span className="muted">空 = 未定价 · 0 = 免费</span>
      </div>
    )
  }

  if (unpriced) {
    return (
      <div className="model-protocols">
        <button
          type="button"
          className="chip-add"
          onClick={open}
          title="填这个条目的四项单价（USD/百万 token）。不填的话，有用量的调用成本一律记 0"
        >
          + 定价
        </button>
        {model.has_usage && (
          <span
            className="tag tag-warn"
            title="这个条目已经有带用量的流水，但四价都没填——那些调用的成本都记成了 0。填上价之后的流水才按价计，不追溯"
          >
            未定价
          </span>
        )}
        {suggestChip}
      </div>
    )
  }

  return (
    <div className="model-protocols">
      <button
        type="button"
        className="model-limit-chip"
        onClick={open}
        title={`单价（USD/百万 token）：${title}。点击修改；改价只影响之后的流水，不追溯`}
      >
        {fmtPrice(current.input)}/{fmtPrice(current.output)}
        <IconPencil />
      </button>
      {(current.cache_read !== null || current.cache_write !== null) && (
        <span className="muted">
          缓存 {fmtPrice(current.cache_read)}/{fmtPrice(current.cache_write)}
        </span>
      )}
      {suggestChip}
    </div>
  )
}

function IconRefresh() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
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
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden>
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
