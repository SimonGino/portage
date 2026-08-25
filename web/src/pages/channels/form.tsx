import { useEffect, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_PATH, PROTOCOL_SOON, declaredProtocols, firstBaseURL } from '../../api'
import type { BaseURLs, Channel, Protocol } from '../../api'
import { Confirm, ErrorBar, Field } from '../../ui'
import { Segmented } from '../../fields'
import { Avatar, vendorForChannel } from '../../icons'

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
}: {
  channel: Channel | null
  /** 新建时是「放弃新建」；编辑时不给（弹框自己有关闭）。 */
  onCancel?: () => void
  onSaved: (id: number) => void
  /** 编辑态的未保存改动上报给弹框——遮罩误点不该把改到一半的表单带走（Dialog guard）。 */
  onDirtyChange?: (dirty: boolean) => void
  /** 编辑时给：删除渠道跟设置同住一个弹框，坐在保存对面。 */
  onDelete?: () => void
}) {
  const [name, setName] = useState(channel?.name ?? '')
  // 每协议一份出站根地址（口径层 v0.96 ②）：**填了哪个协议的地址就是声明了哪个协议**，
  // 没有独立的协议勾选。只在**新建**时出现在这张表单里——编辑走模型页上的「API 地址」
  // 常驻区块（PO 2026-08-20），这儿不再留一份：两处各存一份编辑态，后保存的会把先
  // 保存的悄悄盖回去。
  const [urls, setUrls] = useState<BaseURLs>({})
  // OpenAI Responses 收进「更多设置」（DESIGN.md v0.32 ③）：回退序里它排第二，但
  // 用得最少——折叠的是使用频率不是优先级。填过值就自动展开，免得值被折叠藏住。
  const [more, setMore] = useState(false)
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
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // 空串与非数字都归 0（= 不限）：输入框是 type=number，正常路径进不来非数字。
  const maxConcValue = Number.parseInt(maxConc, 10) > 0 ? Number.parseInt(maxConc, 10) : 0

  const declared = channel ? (channel.protocols ?? []) : declaredProtocols(urls)

  // 能力位只在 Responses 渠道上有意义，所以只有声明了它才露、也只有露着才传（不传 =
  // 那一列不动，同 key_mode 的整体覆盖陷阱）。取消声明 Responses 之后不去清那一列：
  // 清了也读不到，而声明回来时人还得再想一遍这个上游支不支持压缩。
  const showCompaction = declared.includes('openai_responses')
  // 两位一起露一起收：它们问的都是「这个 Responses 上游到底认得什么」。
  const showStateful = showCompaction

  const dirty =
    channel !== null &&
    (name !== channel.name ||
      maxConcValue !== channel.max_concurrency ||
      (showCompaction && compaction !== channel.supports_compaction) ||
      (showStateful && stateful !== channel.supports_stateful_responses))

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  function setURL(p: Protocol, v: string) {
    setUrls((prev) => ({ ...prev, [p]: v }))
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      // key_mode 不在这张表单上（v0.38 ⑨ 的位置已由 v0.44 修订到凭证池），干脆
      // 不传：后端对缺省的 key_mode 是「整列不写」（v0.35 ⑸），比回传 prop 上的
      // 旧值安全——凭证池那边刚改过的话，这儿的 prop 还是老的。
      //
      // disabled 直接从 prop 读、不进表单状态：启停已经挪到右栏抬头那个开关上
      // （v0.46），它改的是同一个字段。存成 state 的话，抬头拨一下、这儿再保存，
      // 就会拿一份挂载时的旧值把刚拨的那下覆盖回去。
      const body = {
        name,
        // 编辑时 base_url 从 prop 读（同 disabled）：它的编辑入口在页面上的
        // 「API 地址」区块，这儿回传旧 state 会把那边刚存的覆盖掉。
        base_url: channel ? channel.base_url : urls,
        max_concurrency: maxConcValue,
        ...(showCompaction ? { supports_compaction: compaction } : {}),
        ...(showStateful ? { supports_stateful_responses: stateful } : {}),
        disabled: channel?.disabled ?? false,
      }
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

  /** 一行端点：协议名做标签，地址值即声明。预览紧随其下（§7：能拼出来的就摆出来）。 */
  function endpointField(p: Protocol, hint?: string) {
    const v = urls[p] ?? ''
    return (
      <Field label={`${PROTOCOL_LABEL[p]} 地址`} hint={hint}>
        <input value={v} onChange={(e) => setURL(p, e.target.value)} placeholder="留空 = 不声明这个协议" />
        {v.trim() !== '' && (
          <div className="baseurl-preview" title="网关实际会请求的完整地址：你填的前缀 + 协议固定子路径">
            → <code>{joinURL(v, PROTOCOL_PATH[p] ?? '')}</code>
          </div>
        )}
      </Field>
    )
  }

  return (
    /* 新建时 form 自己就是那一段，段标题栏在它内部——创建按钮因此能坐在标题右边，
       跟「获取模型列表」同一档位置。编辑时它在弹框里，标题归 Dialog 画，这里不再
       画段头，保存沉到 foot（弹框主按钮右下的通行位）。 */
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
          <input autoFocus={!channel} value={name} onChange={(e) => setName(e.target.value)} />
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

      {/* 端点设置（口径层 v0.96 ②，DESIGN.md v0.32 ③）：每协议一行地址，**填了即声明
          该协议**，没有勾选框。地址存的是「协议子路径之前」的前缀，子路径由网关自己接
          ——每行下面的预览把拼出来的结果直接摆出来，比任何一句「不要带 /v1」都硬。
          OpenAI / Anthropic 常驻，Responses 收「更多设置」。
          编辑态没有这一段（它在页面上的「API 地址」区块里）。 */}
      {!channel && (
        <>
          <div className="form-row">
            {endpointField('openai', '填了即声明该协议，同一上游共用前缀就几行填同一个值')}
            {endpointField('anthropic')}
          </div>
          {!more && !(urls.openai_responses ?? '').trim() ? (
            <button type="button" className="btn btn-quiet" onClick={() => setMore(true)}>
              更多设置（OpenAI Responses{PROTOCOL_SOON.length ? '、' + PROTOCOL_SOON.map((s) => s.label).join('、') : ''}）
            </button>
          ) : (
            <div className="form-row">
              {endpointField(
                'openai_responses',
                PROTOCOL_SOON.length
                  ? PROTOCOL_SOON.map((s) => `${s.label} ${s.hint}`).join('；')
                  : undefined,
              )}
            </div>
          )}
        </>
      )}

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
      {/* 提示语里曾有「只写不回读：保存之后页面上再也看不到它」，v0.47 之后那是假话
          ——建完在「上游凭证」段里能看能复制。 */}
      {!channel && (
        <Field label="上游凭证" hint="先给一份，渠道建完可以在「上游凭证」段里继续加；多给几份就是凭证池，按选取模式轮着用">
          <input
            type="password"
            autoComplete="off"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
          />
        </Field>
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
