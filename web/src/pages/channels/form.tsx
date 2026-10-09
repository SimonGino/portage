import { useEffect, useMemo, useState } from 'react'
import { api, AUTH_SCHEME_OPTIONS, declaredProtocols, firstBaseURL, joinBaseURLs, splitBaseURLs } from '../../api'
import type { AuthScheme, BaseURLDraft, Channel, ChannelPreset } from '../../api'
import { Confirm, ErrorBar, Field } from '../../ui'
import { Picker, Segmented } from '../../fields'
import { Avatar, vendorForChannel } from '../../icons'
import { BaseURLFields } from './baseurl'
import { LoginDialog } from './login'
import { headerRows, headersDirty, headersOf, channelCreatePayload, maxConcurrencyOf, presetPlans, settingsDirty } from './derive'
import type { HeaderRow } from './derive'
import { IconX, IconKey } from '../../icons/acts'
import { providerOptions, useProviders } from '../../prices'

/**
 * joinURL 复刻服务端 `upstream.buildURL` 的拼法：右侧去尾斜杠，直接接子路径。
 *
 * 复刻而不是让服务端回一个预览字段——这一行的用途是**在保存之前**就把 `/v1/v1/…`
 * 摆出来，那一刻服务端还没见过这个地址。两处拼法必须一致，改一处得改另一处。
 */
export function joinURL(baseURL: string, path: string): string {
  return baseURL.trim().replace(/\/+$/, '') + path
}

/**
 * ChannelForm 是渠道的上游设置表单。
 *
 * 新建时右栏整块就是这张表单，此时才摆头像预览——编辑时渠道名与图标已经在右栏
 * 抬头上了，重复一遍只是占地方。编辑时它**装在「上游设置」弹框里**（PO 2026-08-24
 * 裁决，推翻 v0.75 的展开井）：井剩下的内容只有改名、并发、能力位、删除，机制
 * （展开顶开页面、独立保存、收起后靠小圆点提醒未保存）比内容重，而这一页别的
 * 次级操作（管理、检测、挑选）全是弹框，井是仅存的第二种形制。弹框标题由 Dialog
 * 给，编辑态因此不再画自己的段标题，保存沉到右下（同检测弹层的主按钮位）。
 *
 * `key` 由调用方按渠道 id 给：表单是非受控的（useState 初值只在挂载时取一次），
 * 切换渠道而不重挂的话，左边点了别家、右边还留着上一家的输入。
 */
export function ChannelForm({
  channel,
  onCancel,
  onSaved,
  onDirtyChange,
  onDelete,
  onHeadersSaved,
  preset,
  onRepick,
}: {
  channel: Channel | null
  /**
   * 新建时选中的渠道预设（#181）：预填渠道名、三协议地址、厂商标注、认证头，全部可改、
   * 不锁；null / 不给 = 自定义（原空白表单）。图标不另存——渠道图标本就按地址 host 推，
   * 预设地址落在 CHANNEL_HOSTS 里，建出来的渠道自然是那枚图标。
   */
  preset?: ChannelPreset | null
  /** 新建时给：表单顶部「换一个」回预设目录。 */
  onRepick?: () => void
  /** 新建时是「放弃新建」；编辑时不给（弹框自己有关闭）。 */
  onCancel?: () => void
  onSaved: (id: number) => void
  /** 编辑态的未保存改动上报给弹框——遮罩误点不该把改到一半的表单带走（Dialog guard）。 */
  onDirtyChange?: (dirty: boolean) => void
  /** 编辑时给：删除渠道跟设置同住一个弹框，坐在保存对面。 */
  onDelete?: () => void
  /**
   * 编辑时给：头那一笔已落库、后面设置那一笔失败时调，让调用方重拉渠道。不重拉的话
   * 弹框关了再开会摆出旧头，下次改头再把旧值整组写回去（#167）。
   */
  onHeadersSaved?: () => void
}) {
  const plans = preset ? presetPlans(preset) : []
  const [planID, setPlanID] = useState(plans[0]?.id ?? '')
  const plan = plans.find((p) => p.id === planID)
  // 订阅组预设（#216，DESIGN v0.77）：这组没有可填的 key，凭证段是「登录」而非
  // key 输入；建成的渠道带上这个凭证类型。地址与协议集照预设规则可改不锁。
  const credType = preset?.credential_type
  const subscription = credType !== undefined
  const [name, setName] = useState(channel?.name ?? preset?.id ?? '')
  // API 地址是一份共用前缀 + 协议勾选（DESIGN v0.46，口径层 v1.04）；落库前由
  // joinBaseURLs 合回那份「协议 → 地址」map，声明语义照旧由「哪些协议填了地址」
  // 推导。只在**新建**时出现在这张表单里——编辑走模型页上的「API 地址」常驻
  // 区块（PO 2026-08-20），那儿不再留一份：两处各存一份编辑态，后保存的会把先
  // 保存的悄悄盖回去。
  const [draft, setDraft] = useState<BaseURLDraft>(() =>
    plans[0] ? splitBaseURLs(plans[0].protocols) : { shared: '', chips: [], overrides: {} },
  )
  const urls = useMemo(() => joinBaseURLs(draft), [draft])
  // 并发上限（口径层 v0.49）。0 与留空都显示成空——「不限」不该长得像一个数字。
  const [maxConc, setMaxConc] = useState(channel?.max_concurrency ? String(channel.max_concurrency) : '')
  // compaction 能力位（口径层 v0.54）。只在声明了 Responses 时露出来：它问的是「这个
  // 上游认不认 compaction_trigger」，而只有 Responses 透传那条路会去问。
  const [compaction, setCompaction] = useState(channel?.supports_compaction ?? false)
  // 有状态续链能力位（口径层 v0.88）。同样只在声明了 Responses 时露出来，默认取**是**
  // ——与上一位相反：关错会打断一条本来能用的续链，而开错只是让上游自己回一句客户端
  // 读得懂的 not_found。
  const [stateful, setStateful] = useState(channel?.supports_stateful_responses ?? true)
  // 凭证只在**新建**时出现在这张表单里。编辑走凭证池，这样「改个名字」不可能顺手把
  // 凭证清空——后端的修改接口本来就不看这个字段。
  const [credential, setCredential] = useState('')
  // provider 标注（口径层 §2.10，#74）：models.dev 的 id，只服务填价建议与图标分组，
  // 不参与路由。空串 = 未标注，是合法常态（中转站多半对不上任何一家）。
  const [provider, setProvider] = useState(channel?.provider ?? plans[0]?.models_dev ?? '')
  // 认证头写法（口径层 v1.13，#82）。default 即老行为；raw 给 PAI-EAS 这类只认
  // 裸 Authorization 的网关——错配的表象是清一色 401，人会先怀疑凭证本身。
  const [authScheme, setAuthScheme] = useState<AuthScheme>(channel?.auth_scheme ?? preset?.auth_scheme ?? 'default')
  // 额外出站头（#137 / #167）：只在编辑态出现，单独一笔 PUT，其余字段写不碰它。
  const [headerList, setHeaderList] = useState<HeaderRow[]>(() => headerRows(channel?.headers ?? {}))
  // 「登录」开弹层前先建的那只渠道（#216）：null = 还没建。登录弹层按渠道 id 发号，
  // 渠道得先存在——点「登录」就用当前表单值先把渠道建出来，弹层关了再走 onSaved。
  const [loginChannelID, setLoginChannelID] = useState<number | null>(null)
  // 拉失败就只剩「未标注」和当前值可选——标注是可选项，别为它挂错误条。
  const providers = useProviders().list
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const options = useMemo(() => providerOptions(providers, provider), [providers, provider])

  // 空串与非数字都归 0（= 不限）：输入框是 type=number，正常路径进不来非数字。
  const maxConcValue = maxConcurrencyOf(maxConc)

  const declared = channel ? (channel.protocols ?? []) : declaredProtocols(urls)

  // 能力位只在 Responses 渠道上有意义，所以只有声明了它才露、也只有露着才传（不传 =
  // 那一列不动）。取消声明 Responses 之后不去清那一列：清了也读不到，而声明回来时
  // 人还得再想一遍这个上游支不支持压缩（真正的归位由服务端在删协议那一笔上做，#33）。
  const showCompaction = declared.includes('openai_responses')
  // 两位一起露一起收：它们问的都是「这个 Responses 上游到底认得什么」。
  const showStateful = showCompaction

  const settingsChanged =
    channel !== null &&
    settingsDirty(
      channel,
      { name, maxConcurrency: maxConcValue, provider, authScheme, compaction, stateful },
      showCompaction,
    )
  const headersChanged = channel !== null && headersDirty(channel.headers, headerList)
  const dirty = settingsChanged || headersChanged

  // 切套餐 = 换一套地址与它的 models.dev 标注（key 不通用，所以连标注一起换）；
  // 名字、认证头等人可能已改过的字段不动。
  function pickPlan(id: string) {
    const next = plans.find((p) => p.id === id)
    if (!next) return
    setPlanID(id)
    setDraft(splitBaseURLs(next.protocols))
    setProvider(next.models_dev ?? '')
  }

  function setHeaderRow(i: number, patch: Partial<HeaderRow>) {
    setHeaderList((rows) => rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  }

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  // 预设来源条的 key 页（#181）：这套地址的 key 在另一个页面申请时用 plan 那份；
  // 订阅组没有 key 页（#216）——整段链接不渲染。
  const keysURL = plan?.keys_url || preset?.keys_url

  // 点「登录」= 先建渠道再开登录弹层（#216）：弹层按渠道 id 发号，渠道得先存在。
  // 弹层关了（登没登成都算）走 onSaved 跳详情——空渠道在详情页的凭证区块照旧能登
  // （「缺凭证」标记摆着，那儿的「登录」开的是同一个弹层）。
  async function loginAndCreate() {
    setBusy(true)
    setError('')
    try {
      const created = await api.post<{ id: number }>('/channels', createPayload())
      setLoginChannelID(created.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  // 建渠道那笔请求体：「登录」与「创建」共用（#216），差在要不要顺手带 key。
  // 字段汇集在 derive.channelCreatePayload——抽出去为把「订阅预设带
  // credential_type」钉进测试：这一项丢了渠道会静默建成 api_key，登录就开不了。
  function createPayload() {
    return channelCreatePayload({
      name,
      urls,
      maxConcurrency: maxConcValue,
      provider,
      authScheme,
      compaction,
      stateful,
      credentialType: credType,
    })
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    let headersSaved = false
    try {
      if (channel) {
        // 额外出站头是自己那一笔（#167），改了才发、先于设置发：它最可能被闸打回，
        // 打回时什么都还没写。
        if (headersChanged) {
          const { headers, dup } = headersOf(headerList)
          if (dup) throw new Error(`额外出站头 "${dup}" 写了两行，同名只会剩一个`)
          await api.put(`/channels/${channel.id}/headers`, { headers })
          headersSaved = true
        }
        // 编辑走「上游设置」这一笔意图写（#48 批2）：只发这张表单上有的字段，
        // base_url / key_mode / disabled 各有各的写点，这里不回传也回传不了。
        // 能力位只有露着才传（不传 = 那一列不动）。
        if (settingsChanged) {
          await api.put(`/channels/${channel.id}/settings`, {
            name,
            max_concurrency: maxConcValue,
            provider,
            auth_scheme: authScheme,
            ...(showCompaction ? { supports_compaction: compaction } : {}),
            ...(showStateful ? { supports_stateful_responses: stateful } : {}),
          })
        }
        onSaved(channel.id)
      } else {
        const created = await api.post<{ id: number }>('/channels', {
          ...createPayload(),
          credential,
        })
        onSaved(created.id)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      if (headersSaved) onHeadersSaved?.()
    } finally {
      setBusy(false)
    }
  }

  return (
    /* 新建时 form 自己就是那一段，段标题栏在它内部——创建按钮因此能坐在标题右边。
       编辑时它在弹框里，标题归 Dialog 画，这里不再画段头，保存沉到 foot（弹框主按钮
       右下的通行位）。 */
    <form className={channel ? 'form' : 'section form'} onSubmit={submit}>
      {!channel && (
        <header className="section-head">
          <h2>新建渠道</h2>
          <div className="row-actions">
            {onCancel && (
              <button type="button" className="btn btn-quiet" onClick={onCancel}>
                取消
              </button>
            )}
            <button
              className="btn btn-primary"
              disabled={busy || !name.trim() || declared.length === 0}
            >
              {busy ? '保存中…' : '创建'}
            </button>
          </div>
        </header>
      )}

      {/* 预设来源条（DESIGN v0.72）：哪条预设预填的、换一个回目录、去哪拿 key。
          自定义只给回目录那一下。 */}
      {!channel && onRepick && (
        <div className="preset-from">
          {preset ? (
            <>
              <span>
                来自预设 <strong>{preset.name}</strong>
                {preset.note && <span className="muted"> · {preset.note}</span>}
              </span>
              <button type="button" className="bar-link" onClick={onRepick}>
                换一个
              </button>
              <span className="spacer" />
              {keysURL && (
                <a href={keysURL} target="_blank" rel="noreferrer">
                  去拿 key
                </a>
              )}
            </>
          ) : (
            <>
              <span>自定义（空白表单）</span>
              <button type="button" className="bar-link" onClick={onRepick}>
                回目录选预设
              </button>
            </>
          )}
        </div>
      )}
      {!channel && plans.length > 1 && (
        <Field label="套餐" hint="同一家的几套地址，key 不通用：用哪套就贴哪套的 key。切换会换掉下面的地址与厂商标注">
          <Segmented
            value={planID}
            options={plans.map((p) => ({ value: p.id, label: p.name }))}
            onChange={pickPlan}
          />
        </Field>
      )}

      {/* 图标是从地址的 host 猜出来的（渠道没有「供应商」这个字段）。
          边填边显示，等于顺手校验了域名有没有填错——图标一直是首字母块，
          多半是地址还没填对。 */}
      {!channel && (
        <div className="form-preview">
          <Avatar vendor={vendorForChannel({ name, base_url: urls })} fallback={name || '?'} size={40} />
          <div>
            <div className="form-preview-name">{name || '未命名渠道'}</div>
            <div className="muted">{firstBaseURL(urls) || '还没填协议地址'}</div>
          </div>
        </div>
      )}

      {/* 名字与并发并排：一宽一窄，各占一行是浪费。 */}
      <div className="form-row">
        <Field label="渠道名" hint="限定名的前半截（如 bailian/qwen3-max），不能含 `/`">
          <input autoFocus={!channel && !preset} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="并发上限" hint="同时打向这个上游的请求数上限，超出的在网关排队；留空 = 不限">
          <input
            type="number"
            min={0}
            step={1}
            placeholder="不限"
            value={maxConc}
            onChange={(e) => setMaxConc(e.target.value)}
          />
        </Field>
      </div>

      {/* 厂商标注（口径层 §2.10，#74）：选了才有模型页的 models.dev 建议价。
          不参与路由、不校验取值——中转站对不上任何一家时就留「未标注」。 */}
      <Field
        label="厂商标注"
        hint="这个上游对应 models.dev 的哪一家，只用来给模型页出建议价与图标分组，不影响转发；中转站对不上就留「未标注」"
      >
        <Picker value={provider} options={options} onChange={setProvider} placeholder="未标注" />
      </Field>

      {/* 认证头写法（口径层 v1.13，#82）。默认按协议惯例（anthropic 发 x-api-key、
          openai 侧发 Bearer），PAI-EAS 这类只认裸 Authorization 的网关选「裸」——
          错配的表象是清一色 401，提示语把这条因果直接说出来。 */}
      <Field
        label="认证头"
        hint="凭证以哪种头发给上游：默认按协议惯例（anthropic 发 x-api-key，openai 发 Authorization: Bearer）；自部署网关（如阿里 PAI-EAS）只认 Authorization 裸凭证时选「裸 Authorization」——选错的表现是上游一律回 401"
      >
        <Segmented value={authScheme} options={AUTH_SCHEME_OPTIONS} onChange={setAuthScheme} />
      </Field>

      {/* 额外出站头（#167，DESIGN §5.1）：键值对逐行，值旁删行微钮，底下「添加一行」。
          闸全在服务端 ValidateHeaders，拒因原文进 ErrorBar。不用 Field：它是 <label>，
          一个 label 里装多个输入框会把点击都导向第一个。 */}
      {channel && (
        <div className="field">
          <span className="field-label">额外出站头</span>
          {headerList.map((r, i) => (
            <div className="header-row" key={i}>
              <input
                className="mono"
                aria-label="头名"
                placeholder="头名，如 x-opencode-session"
                value={r.name}
                onChange={(e) => setHeaderRow(i, { name: e.target.value })}
              />
              <input
                aria-label="值"
                placeholder="值"
                value={r.value}
                onChange={(e) => setHeaderRow(i, { value: e.target.value })}
              />
              <button
                type="button"
                className="act-icon"
                aria-label="删掉这一行"
                title="删掉这一行"
                onClick={() => setHeaderList((rows) => rows.filter((_, j) => j !== i))}
              >
                <IconX />
              </button>
            </div>
          ))}
          <div>
            <button
              type="button"
              className="btn btn-quiet"
              onClick={() => setHeaderList((rows) => [...rows, { name: '', value: '' }])}
            >
              添加一行
            </button>
          </div>
          <span className="field-hint">
            固定随转发、检测、拉模型列表一起发给上游的请求头，给「缺某个头就拒」的上游用（如 OpenCode Go 的
            x-opencode-session）；凭证、Content-Type、Host 这类网关自己写的头不能填。值明文显示，不是放密钥的地方
          </span>
        </div>
      )}

      {/* 端点设置（DESIGN v0.46，口径层 v1.04）：一份共用前缀 + 协议勾选即声明，
          个别协议要不同前缀时在预览行上单独填。地址存的是「协议子路径之前」的前缀，
          子路径由网关自己接——每行预览把拼出来的结果直接摆出来，比任何一句
          「不要带 /v1」都硬。三个协议都是一颗 chip，不再折叠（v0.32 ③ 折的是
          每协议一行的地址框，chips 行三颗同宽，没有可折叠的体积）。
          编辑态没有这一段（它在页面上的「API 地址」区块里，同一控件）。 */}
      {!channel && <BaseURLFields draft={draft} onChange={setDraft} />}

      {/* Codex 压缩能力位（口径层 v0.54）。默认「不支持」，得人明确勾——上游认不认
          compaction_trigger 网关探不出来，而猜错的代价是 Codex 在长会话里直接 Fatal。 */}
      {showCompaction && (
        <Field
          label="Codex 压缩（remote compaction）"
          hint="这个上游认不认 Responses 请求里的 compaction_trigger。说「不支持」时，压缩请求会被网关明确拒绝，而不是转发出去让 Codex 收到空压缩结果后当场失败"
        >
          <Segmented
            value={compaction ? 'yes' : 'no'}
            options={[
              { value: 'yes', label: '支持' },
              { value: 'no', label: '不支持' },
            ]}
            onChange={(v) => setCompaction(v === 'yes')}
          />
        </Field>
      )}
      {/* Responses 有状态续链能力位（口径层 v0.88）。默认「支持」，只有明确知道这个
          上游不做有状态续链时才关——关错会把一条本来能用的续链打断，而开错只是让上游
          自己回一句客户端读得懂的错误。 */}
      {showStateful && (
        <Field
          label="Responses 有状态续链（previous_response_id）"
          hint="这个上游认不认 Responses 请求里的 previous_response_id。说「不支持」时，带这个字段的请求会被网关明确拒绝并让客户端重发完整 input；跨协议转换那条路无论这里怎么选都一律拒绝"
        >
          <Segmented
            value={stateful ? 'yes' : 'no'}
            options={[
              { value: 'yes', label: '支持' },
              { value: 'no', label: '不支持' },
            ]}
            onChange={(v) => setStateful(v === 'yes')}
          />
        </Field>
      )}
      {/* 凭证段按预设切（#216，DESIGN v0.77）：订阅组没有可填的 key，是同一颗「登录」
          （与渠道页凭证区块那颗同形制）——点它先用当前表单值建渠道，再开登录弹层；
          也可以先「创建」空渠道稍后登（「缺凭证」标记照旧）。 */}
      {!channel && subscription && credType && (
        <div className="field">
          <span className="field-label">上游凭证</span>
          <div className="cred-add">
            <button
              type="button"
              className="act"
              disabled={busy || !name.trim() || declared.length === 0}
              onClick={() => void loginAndCreate()}
            >
              <IconKey />
              登录
            </button>
            <span className="muted">
              订阅渠道的凭证由账号登录产生，不贴 key；点「登录」先建渠道再弹登录弹层，也可以先建空渠道稍后登
            </span>
          </div>
        </div>
      )}
      {!channel && !subscription && (
        /* 提示语里曾有「只写不回读：保存之后页面上再也看不到它」，v0.47 之后那是
           假话——建完在「上游凭证」段里能看能复制。 */
        <Field label="上游凭证" hint="先给一份，渠道建完可以在「上游凭证」段里继续加；多给几份就是凭证池，按选取模式轮着用">
          <input
            // 选了预设，表单里只剩凭证要人补（口径层 v1.47），焦点直接落这儿。
            autoFocus={!!preset}
            type="password"
            autoComplete="off"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
          />
        </Field>
      )}
      {/* 「登录」先建的那只渠道上的登录弹层：关了就跳详情（登没登成都算建成）。 */}
      {!channel && loginChannelID !== null && credType && (
        <LoginDialog
          channelID={loginChannelID}
          credentialType={credType}
          replaceCred={null}
          onClose={() => {
            const id = loginChannelID
            setLoginChannelID(null)
            onSaved(id)
          }}
        />
      )}
      <ErrorBar message={error} />
      {channel && (
        /* 删除与保存同住一行、各占一头：删除是这个弹框里唯一不属于「设置」的动作，
           放左边 ghost 起步（Confirm 两段式），不跟主按钮挤在一起。 */
        <div className="settings-foot">
          {onDelete ? <Confirm ghost label="删除渠道" onConfirm={onDelete} /> : <span />}
          <button className="btn btn-primary" disabled={busy || !name.trim() || !dirty}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      )}
    </form>
  )
}
