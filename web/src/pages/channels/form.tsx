import { useEffect, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_PATH, PROTOCOL_SOON } from '../../api'
import type { Channel, Protocol } from '../../api'
import { ErrorBar, Field } from '../../ui'
import { Segmented, SegmentedMulti } from '../../fields'
import { Avatar, vendorForChannel } from '../../icons'

const PROTOCOLS: Protocol[] = ['anthropic', 'openai', 'openai_responses']

/**
 * joinURL 复刻服务端 `upstream.buildURL` 的拼法：右侧去尾斜杠，直接接子路径。
 *
 * 复刻而不是让服务端回一个预览字段——这一行的用途是**在保存之前**就把 `/v1/v1/…`
 * 摆出来，那一刻服务端还没见过这个 base_url。两处拼法必须一致，改一处得改另一处。
 */
function joinURL(baseURL: string, path: string): string {
  return baseURL.trim().replace(/\/+$/, '') + path
}

/**
 * ChannelForm 是渠道的上游设置表单。
 *
 * 它**长在右栏里**，不再是弹框（主从两栏之后弹框没有存在理由：右栏本来就是这个渠道
 * 的地方，再盖一层框只是把同一份内容挪到屏幕中间，还顺手挡住左边的列表）。新建时
 * 右栏整块就是这张表单，此时才摆头像预览——编辑时渠道名与图标已经在右栏抬头上了，
 * 重复一遍只是占地方。
 *
 * `key` 由调用方按渠道 id 给：表单是非受控的（useState 初值只在挂载时取一次），
 * 切换渠道而不重挂的话，左边点了别家、右边还留着上一家的输入。
 */
export function ChannelForm({
  channel,
  onCancel,
  onSaved,
}: {
  channel: Channel | null
  /** 新建时是「放弃新建」；编辑时不给（表单是常驻的，没有可取消的东西）。 */
  onCancel?: () => void
  onSaved: (id: number) => void
}) {
  const [name, setName] = useState(channel?.name ?? '')
  // 支持协议集（口径层 v0.33）。默认只勾 OpenAI：绝大多数上游只提供它，多勾一个探测
  // 不过反而要人回来改。
  const [protos, setProtos] = useState<Protocol[]>(
    channel?.protocols?.length ? channel.protocols : ['openai'],
  )
  const [baseURL, setBaseURL] = useState(channel?.base_url ?? '')
  // 并发上限（口径层 v0.49）。0 与留空都显示成空——「不限」不该长得像一个数字。
  const [maxConc, setMaxConc] = useState(channel?.max_concurrency ? String(channel.max_concurrency) : '')
  // compaction 能力位（口径层 v0.54）。只在勾了 Responses 时露出来：它问的是「这个
  // 上游认不认 compaction_trigger」，而只有 Responses 透传那条路会去问。
  const [compaction, setCompaction] = useState(channel?.supports_compaction ?? false)
  // 凭证只在**新建**时出现在这张表单里。编辑走凭证池，这样「改个名字」不可能顺手把
  // 凭证清空——后端的修改接口本来就不看这个字段。
  const [credential, setCredential] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)

  // 「已保存」两秒后自己消失。表单常驻在右栏里，保存之后页面上什么都不动，
  // 没有一句回执的话人分不清是存上了还是按钮没响应。
  useEffect(() => {
    if (!saved) return
    const t = setTimeout(() => setSaved(false), 2000)
    return () => clearTimeout(t)
  }, [saved])

  // 空串与非数字都归 0（= 不限）：输入框是 type=number，正常路径进不来非数字。
  const maxConcValue = Number.parseInt(maxConc, 10) > 0 ? Number.parseInt(maxConc, 10) : 0

  // 能力位只在 Responses 渠道上有意义，所以只有勾了它才露、也只有露着才传（不传 =
  // 那一列不动，同 key_mode 的整体覆盖陷阱）。取消勾 Responses 之后不去清那一列：
  // 清了也读不到，而勾回来时人还得再想一遍这个上游支不支持压缩。
  const showCompaction = protos.includes('openai_responses')

  const dirty =
    channel !== null &&
    (name !== channel.name ||
      baseURL !== channel.base_url ||
      maxConcValue !== channel.max_concurrency ||
      (showCompaction && compaction !== channel.supports_compaction) ||
      protos.join(',') !== (channel.protocols ?? []).join(','))

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
        protocols: protos,
        base_url: baseURL,
        max_concurrency: maxConcValue,
        ...(showCompaction ? { supports_compaction: compaction } : {}),
        disabled: channel?.disabled ?? false,
      }
      if (channel) {
        await api.put(`/channels/${channel.id}`, body)
        setSaved(true)
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
    /* form 自己就是那一段，段标题栏在它内部——保存按钮因此能坐在标题右边，跟
       「检测」「获取模型列表」同一档位置，省掉表单底下那条只装一颗按钮的行。 */
    <form className="section form" onSubmit={submit}>
      <header className="section-head">
        <h2>{channel ? '上游设置' : '新建渠道'}</h2>
        <div className="row-actions">
          {saved && <span className="muted">已保存</span>}
          {onCancel && (
            <button type="button" className="btn btn-quiet" onClick={onCancel}>
              取消
            </button>
          )}
          <button className="btn btn-primary" disabled={busy || !name.trim() || (channel !== null && !dirty)}>
            {busy ? '保存中…' : channel ? '保存' : '创建'}
          </button>
        </div>
      </header>

      {/* 图标是从 base_url 的 host 猜出来的（渠道没有「供应商」这个字段）。
          边填边显示，等于顺手校验了域名有没有填错——图标一直是首字母块，
          多半是 base_url 还没填对。 */}
      {!channel && (
        <div className="form-preview">
          <Avatar vendor={vendorForChannel({ name, base_url: baseURL })} fallback={name || '?'} size={40} />
          <div>
            <div className="form-preview-name">{name || '未命名渠道'}</div>
            <div className="muted">{baseURL || '还没填 base_url'}</div>
          </div>
        </div>
      )}

      {/* 名字与协议集并排：一个短输入 + 三颗按钮，各自占一整行是纯浪费。 */}
      <div className="form-row">
        <Field label="渠道名" hint="限定名的前半截（如 bailian/qwen3-max），不能含 `/`">
          <input autoFocus={!channel} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="支持的上游协议" hint="这个上游能说的都勾上，不必按协议拆成两个渠道">
          <SegmentedMulti
            value={protos}
            onChange={setProtos}
            options={PROTOCOLS.map((p) => ({
              value: p,
              label: PROTOCOL_LABEL[p],
              // 子路径只进 title，不摆在按钮里：下面那行完整地址预览已经把它显示
              // 出来了，而且显示的是真会被请求的那一串；摆在这儿只会把三颗按钮
              // 撑到换行。
              title: PROTOCOL_PATH[p],
            }))}
            soon={PROTOCOL_SOON}
          />
        </Field>
      </div>
      {/* base_url 存的是「协议子路径之前」的前缀，子路径由网关自己接。这里不写
          「不要带 /v1」那句提示了——下面那行预览把拼出来的结果直接摆出来，比任何
          一句提示都硬。并发上限与它并排：一宽一窄，各占一行是浪费。 */}
      <div className="form-row">
        <Field label="Base URL">
          <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
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
      {/* 边填边把拼出来的完整地址摆出来。这是 base_url 那个必踩的坑唯一说得清的
          方式——上面那句提示写了「不带 /v1」，但人是照着上游文档粘的，粘进来的多半
          就带；只有把 `…/v1/v1/chat/completions` 摆在眼前，那句提示才真的被读到。 */}
      {baseURL.trim() !== '' && protos.length > 0 && (
        <div className="url-preview">
          {protos.map((p) => (
            <div key={p}>
              <span className="muted">预览 {PROTOCOL_LABEL[p]}：</span>
              <code>{joinURL(baseURL, PROTOCOL_PATH[p] ?? '')}</code>
            </div>
          ))}
        </div>
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
    </form>
  )
}
