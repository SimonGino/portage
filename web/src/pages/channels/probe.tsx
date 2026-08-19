import { PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type { ModelProbeRow, ProbeGroup } from '../../api'

/**
 * ProbeRow 是一份凭证的探测结论（口径层 v0.33 / v0.38，三态见 v0.89）。
 *
 * 四段而不是两段：全通、子路径不存在、**没拿到响应**、子路径存在但上游回了非 2xx。
 *
 * 「没拿到响应」单列是 v0.89 的口径修正：它原先并进「不存在」那一段，于是超时被
 * 照实画成「这个协议的客户端打过来会 404」——而实测有上游（EAS 上挂的 llm-gateway）
 * 对探测用的空 `{}` 既不 400 也不 401，直接挂着不回，它的子路径其实条条可用。
 * 所以这一段也不转警告色：只有确定的「不通」才配亮灯，与模型矩阵同一条纪律。
 *
 * 非 2xx 那一段也不是哑的：`Probe` 把 404/405 之外的一切都判存在（那正是它的判据
 * ——任何真实上游都会拿 400/401 回绝一个空 `{}`，而那恰恰证明路由存在），于是一个
 * 每条子路径都回 401 的渠道在页面上显示「探测通过」。路由确实通，凭证确实是坏的，
 * 两句话都得说。400 不算：空 `{}` 被拒是这次探测的正常结果，不是异常。
 */
export function ProbeRow({ group, multi }: { group: ProbeGroup; multi: boolean }) {
  const missing = group.results.filter((r) => r.state === 'missing')
  const unclear = group.results.filter((r) => r.state === 'unclear')
  // 401/403/429/5xx：路由在，但这一把凭证这一刻打过去是这个下场。
  const notable = group.results.filter((r) => r.state === 'ok' && r.status > 400)
  const who = group.credential ? group.credential + (group.disabled ? '（已停用）' : '') : ''
  const prefix = multi && who ? `${who}：` : ''
  const listed = [...missing, ...unclear, ...notable]

  return (
    <div className={'probe' + (missing.length > 0 || notable.length > 0 ? ' probe-bad' : '')}>
      <span>
        {prefix}
        {missing.length > 0
          ? `探测未通过 ${missing.length} 项——只是提示，不影响保存与路由，但这些协议的客户端打过来会 404：`
          : unclear.length > 0
            ? `${unclear.length} 项说不清：上游没回话。可能是网络不通，也可能是这家上游对探测用的空请求体不作答——后者不影响真实请求，跑一次模型探测就能分辨：`
            : notable.length === 0
              ? `探测通过：勾选的 ${group.results.length} 个协议子路径上游都有`
              : `子路径都在，但上游回了非 2xx——路由没问题，是这把凭证或上游状态的问题：`}
      </span>
      {listed.length > 0 && (
        <ul>
          {listed.map((r) => (
            <li key={r.protocol}>
              <code>{PROTOCOL_PATH[r.protocol] ?? r.protocol}</code> {r.detail}
              {/* 只给「不存在」补状态码：非 2xx 那段的固定词表自带括号里的数字，
                  「说不清」压根没有状态码可补（status 是 0）。 */}
              {r.state === 'missing' && r.status > 0 && ` (HTTP ${r.status})`}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/**
 * ModelProbeGrid 是模型级探测的结论（口径层 v0.43）：每个启用中的纳管模型一行，
 * 它的有效协议集里每一侧一格。
 *
 * 三态不是二态：把 429 画成 ✗、把 400 画成 ✓ 都是撒谎，而探测的口径是只提示——
 * 提示就得诚实。「说不清」画 ?，状态码摆出来，判断留给人。符号是非颜色线索
 * （DESIGN.md §3：语义色必须配一个不靠颜色的线索）。
 *
 * 非通的格子把摘要**摆在屏幕上**，不只挂 title：`? 429` 这四个字符本身不解释任何
 * 事，而 tooltip 里的东西等于没写——没人会去悬停一片自己看着还行的网格。摘要用的
 * 仍是我方固定词表，不带上游原文（v0.43 ②）。
 */
export function ModelProbeGrid({ rows, credential }: { rows: ModelProbeRow[]; credential: string }) {
  // 只有确定的「不通」才把左线转警告色；「说不清」不算——凭证 401 时整个矩阵都是
  // 说不清，把它画成警告等于每次都在喊狼来了。
  const bad = rows.some((r) => r.results.some((x) => x.state === 'missing'))
  return (
    <div className={'probe' + (bad ? ' probe-bad' : '')}>
      <span>
        模型探测{credential ? `（用「${credential}」）` : ''}：每格发了一个带模型名的最小真实请求
        ——只提示，不落库也不影响路由
      </span>
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
