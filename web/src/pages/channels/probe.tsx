import { useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_ORDER, PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type { Channel, ChannelProbe, Credential, ModelProbeRow, Protocol } from '../../api'
import { Dialog, Field } from '../../ui'
import { Picker } from '../../fields'
import { ModelIcon } from '../../icons'

/**
 * ProbeDialog 是检测弹层（口径层 v0.96 ③，DESIGN v0.32 ①）：发起、勾选、结果都在
 * 这一个容器里，关弹层即失——检测是一次性采样，页面上不再有常驻探测区块。
 *
 * 两处入口开的是同一个弹层，只差预选哪把凭证：凭证行「检测」预选当前在用的那把，
 * 「管理」弹框每行的检测预选那一把（含已停用——恢复是纯人工的，「这把还坏不坏」
 * 除了发一次请求没有别的办法回答）。
 *
 * 协议勾选默认只勾 openai（不为用不上的协议侧多花 token），声明集里没有 openai 时
 * 按回退序取命中的第一个——与出站协议选择同一条秩序，不另发明。勾选不落库。
 */
export function ProbeDialog({
  channel,
  credentials,
  initial,
  onClose,
}: {
  channel: Channel
  credentials: Credential[]
  /** 预选的那把凭证。 */
  initial: Credential
  onClose: () => void
}) {
  const declared = (channel.protocols ?? []) as Protocol[]
  const models = (channel.models ?? []).filter((m) => !m.disabled)
  const fallback = PROTOCOL_ORDER.filter((p) => declared.includes(p))
  const defaultProto = fallback.includes('openai') ? 'openai' : fallback[0]

  const [credID, setCredID] = useState(initial.id)
  // 空串 = 全部纳管模型；非空 = 单选那一个（新加一个模型不用整个矩阵重跑）。
  const [model, setModel] = useState('')
  const [picked, setPicked] = useState<Protocol[]>(defaultProto ? [defaultProto] : [])
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<ChannelProbe | null>(null)
  const [error, setError] = useState('')

  /** 改任何一个勾选就把旧结果撤下来：矩阵旁边摆着一套已经不对应的参数是撒谎。 */
  function reset() {
    setResult(null)
    setError('')
  }

  function toggleProto(p: Protocol) {
    reset()
    // 勾上时过一遍 fallback 归一顺序：矩阵列序跟回退序走，不跟点击顺序跳。
    setPicked((prev) =>
      prev.includes(p) ? prev.filter((x) => x !== p) : fallback.filter((x) => prev.includes(x) || x === p),
    )
  }

  async function run() {
    setRunning(true)
    setError('')
    setResult(null)
    try {
      const r = await api.post<ChannelProbe>(`/channels/${channel.id}/probe`, {
        credential_id: credID,
        model,
        protocols: picked,
      })
      setResult(r)
    } catch (e) {
      // 措辞纪律（口径层 v0.96 ③）：失败永远是「本次检测失败」，不定性「不支持」
      // ——检测是一次采样，限流、上游抖动都会让它失败，定性交给人。
      setError('本次检测失败：' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setRunning(false)
    }
  }

  return (
    <Dialog title="检测" wide onClose={onClose}>
      <div className="probe-dialog">
        <p className="muted">
          每格发一个带模型名的最小真实请求（max_tokens 压到最小）——只提示，不落库也不影响路由；结果关掉这个框就没了。
        </p>

        {/* 控件走 Picker 不走原生 select（fields.tsx 的通则）：凭证行要带停用标注，
            模型多了要能搜。 */}
        <Field label="凭证">
          <Picker
            value={credID}
            options={credentials.map((c) => ({
              value: c.id,
              label: c.name,
              hint: c.disabled ? '已停用' : undefined,
            }))}
            onChange={(v) => {
              reset()
              setCredID(v)
            }}
          />
        </Field>

        <Field label="模型">
          <Picker
            value={model}
            options={[
              { value: '', label: `全部纳管模型（${models.length} 个）` },
              ...models.map((m) => ({
                value: m.upstream_model,
                label: m.upstream_model,
                icon: <ModelIcon model={m.upstream_model} size={16} />,
              })),
            ]}
            onChange={(v) => {
              reset()
              setModel(v)
            }}
          />
        </Field>

        <Field label="协议" hint="可选项 = 已声明协议。想测别的协议，先在「API 地址」给它填一行地址">
          <div className="probe-protos">
            {fallback.map((p) => (
              <button
                key={p}
                type="button"
                className={'chip-toggle' + (picked.includes(p) ? ' is-on' : '')}
                onClick={() => toggleProto(p)}
                title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}
              >
                {PROTOCOL_SHORT[p] ?? p}
              </button>
            ))}
          </div>
        </Field>

        <div className="probe-dialog-run">
          <button
            type="button"
            className="btn"
            onClick={() => void run()}
            disabled={running || picked.length === 0 || models.length === 0}
          >
            {running ? '检测中…' : '检测'}
          </button>
          {models.length === 0 && <span className="muted">还没有启用中的纳管模型，没有可检测的目标。</span>}
          {models.length > 0 && picked.length === 0 && <span className="muted">至少勾一个协议。</span>}
        </div>

        {error && <div className="bar bar-warn">{error}</div>}
        {result && <ProbeMatrix rows={result.models} credential={result.credential} />}
      </div>
    </Dialog>
  )
}

/**
 * ProbeMatrix 是检测结论：每个选中的模型一行，勾选的协议每一侧一格。
 *
 * 三态不是二态：把 429 画成 ✗、把 400 画成 ✓ 都是撒谎，而检测的口径是只提示——
 * 提示就得诚实。「说不清」画 ?，状态码摆出来，判断留给人。符号是非颜色线索
 * （DESIGN.md §3：语义色必须配一个不靠颜色的线索）。
 *
 * 非通的格子把摘要**摆在屏幕上**，不只挂 title：`? 429` 这四个字符本身不解释任何
 * 事，而 tooltip 里的东西等于没写——没人会去悬停一片自己看着还行的网格。摘要用的
 * 仍是我方固定词表，不带上游原文（v0.43 ②）；403 注明用的是哪把凭证——它有
 * 「这把凭证没开通这个模型」的含义。
 */
function ProbeMatrix({ rows, credential }: { rows: ModelProbeRow[]; credential: string }) {
  // 只有确定的「不通」才把左线转警告色；「说不清」不算——凭证 401 时整个矩阵都是
  // 说不清，把它画成警告等于每次都在喊狼来了。
  const bad = rows.some((r) => r.results.some((x) => x.state === 'missing'))
  return (
    <div className={'probe' + (bad ? ' probe-bad' : '')}>
      <span>用「{credential}」检测的结果：</span>
      <ul className="probe-models">
        {rows.map((r) => {
          const off = r.results.filter((x) => x.state !== 'ok')
          return (
            <li key={r.model}>
              <div className="probe-model-row">
                <code>{r.model}</code>
                {r.results.map((x) => (
                  <span key={x.protocol} className={'probe-cell probe-' + x.state}>
                    {PROTOCOL_SHORT[x.protocol] ?? x.protocol}{' '}
                    {x.state === 'ok' ? '✓' : x.state === 'missing' ? '✗' : `? ${x.status || '—'}`}
                  </span>
                ))}
              </div>
              {off.length > 0 && (
                <div className="probe-why">
                  {off.map((x) => (
                    /* 不再补一次 HTTP 码：格子里已经写着 `? 401`，固定词表那句话
                       自己也带着括号里的数字，三处同一个数只是噪音。 */
                    <span key={x.protocol}>
                      {PROTOCOL_SHORT[x.protocol] ?? x.protocol}：{x.detail}
                      {x.status === 403 && `（用的是「${credential}」）`}
                    </span>
                  ))}
                </div>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
