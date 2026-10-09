// 模型页的纯推导。**这个文件不 import React**：清单排序、协议子集归一、
// 「证据不全不推断」、上限与模型名的解析——这些是「算错了且看不出来」的一类，
// 抽成纯函数才测得动（derive.test.ts；照排行页 intervals.ts 成例，#56）。
// 状态与网络在 useChannel.tsx，JSX 只负责摆。

import { PROTOCOL_ORDER, declaredProtocols } from '../../api'
import type {
  AuthScheme,
  BaseURLs,
  Channel,
  ChannelModel,
  ChannelPreset,
  Credential,
  CredentialType,
  ModelListResult,
  Protocol,
  PresetPlan,
} from '../../api'

// ── 左栏清单 ─────────────────────────────────────────────────────────────

/** 停用的沉底，其余按 id（即接入先后）排：左栏是天天扫的清单，停用项混在中间
 *  每次都要跳读。组内都按 id，稳定不随改名跳位。 */
export function sortChannels(list: readonly Channel[]): Channel[] {
  return [...list].sort((a, b) => Number(a.disabled) - Number(b.disabled) || a.id - b.id)
}

/** 搜渠道名或**任意一个协议**的地址（口径层 v0.96）：同一家上游可能只有某一协议
 *  挂在特征域名下，只搜第一份会漏。空查询原样回。 */
export function filterChannels(list: readonly Channel[], query: string): Channel[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...list]
  return list.filter(
    (c) =>
      c.name.toLowerCase().includes(q) ||
      Object.values(c.base_url ?? {}).some((u) => (u ?? '').toLowerCase().includes(q)),
  )
}

/** 清单行尾的一字标记：停用 > 缺凭证 > 无协议，正常为空串。三档互斥，只摆最要紧的。 */
export function channelMark(ch: Channel): '停用' | '缺凭证' | '无协议' | '' {
  if (ch.disabled) return '停用'
  if (ch.enabled_keys === 0) return '缺凭证'
  if ((ch.protocols ?? []).length === 0) return '无协议'
  return ''
}

// ── 上游列表给的证据 ──────────────────────────────────────────────────────

/** 上游在哪些协议侧列出了这个模型，只保留渠道自己声明的协议。**只用于给建议**，
 *  不自动改配置——拉回来的列表可能是中转站写死的（口径层 v0.40），采信它等于把
 *  探测做成了闸。没拉过回空数组。 */
export function listedOn(
  listed: readonly ModelListResult[] | null,
  channelProtocols: readonly Protocol[],
  model: string,
): Protocol[] {
  if (!listed) return []
  return listed
    .filter((r) => (r.models ?? []).includes(model))
    .flatMap((r) => r.protocols)
    .filter((p) => channelProtocols.includes(p))
}

/**
 * 渠道的每一个协议侧都真拉到了一份列表。**证据不全就不推断子集**：`models` 为
 * null 是「这一侧没拉到」（401、超时、回的不是 JSON），与「拉到了但没列出它」在
 * 证据上是两回事，而 listedOn 把两者压成了同一个「不在里面」。按后者写库，等于凭
 * 零证据砍掉一条本来可能原生可走的协议路径，把请求推去做有损转换——比没推断坏得多。
 */
export function listComplete(
  listed: readonly ModelListResult[] | null,
  channelProtocols: readonly Protocol[],
): boolean {
  return (
    listed !== null &&
    channelProtocols.every((p) => listed.some((r) => r.models !== null && r.protocols.includes(p)))
  )
}

/** 从挑选面板加进来时给模型带的协议子集：证据齐全且上游**只在一部分**侧列出它，
 *  才写那一部分；否则写空 = 继承渠道全集。 */
export function protocolsToAdd(
  on: readonly Protocol[],
  complete: boolean,
  channelProtocols: readonly Protocol[],
): Protocol[] {
  return complete && on.length < channelProtocols.length ? [...on] : []
}

/** 「上游只在 X 侧列出 · 采纳」的建议：只在证据齐全、且确实只列出了一部分时给；
 *  全列出或一侧都没列都不建议。 */
export function suggestProtocols(
  on: readonly Protocol[],
  complete: boolean,
  channelProtocols: readonly Protocol[],
): Protocol[] | null {
  return complete && on.length > 0 && on.length < channelProtocols.length ? [...on] : null
}

// ── 模型的协议子集 ──────────────────────────────────────────────────────

/** 渠道协议集缩小之后，模型上没跟着改的那些值会留在这儿（宽松存，口径层 v0.40）。
 *  照实列出而不是悄悄滤掉：它们此刻确实让这个模型不可用。 */
export function staleProtocols(
  current: readonly Protocol[],
  channelProtocols: readonly Protocol[],
): Protocol[] {
  return current.filter((p) => !channelProtocols.includes(p))
}

/**
 * 勾选结果归一成落库值。全勾等价于继承，所以勾满时归成空数组，不在库里留一份跟
 * 渠道集重复的冗余——那份冗余会在渠道日后加一个协议时，悄悄把新协议挡在这个模型
 * 外面。但**还留着失效项时不归零**：归零会把它们一并抹掉，而那是人没点过的东西。
 */
export function normalizeProtocols(
  next: readonly Protocol[],
  channelProtocols: readonly Protocol[],
): Protocol[] {
  const inChannel = channelProtocols.filter((p) => next.includes(p))
  const rest = next.filter((p) => !channelProtocols.includes(p))
  return inChannel.length === channelProtocols.length && rest.length === 0 ? [] : [...inChannel, ...rest]
}

/** 两个协议集合是否同一集合（不论顺序）。 */
export function sameProtocols(a: readonly Protocol[], b: readonly Protocol[]): boolean {
  return a.length === b.length && a.every((p) => b.includes(p))
}

// ── 输入上限 ─────────────────────────────────────────────────────────────

/** 输入上限显示成 200k 这种紧凑形；不整千的照原样带分隔符摆。 */
export function fmtTokens(n: number): string {
  return n >= 1000 && n % 1000 === 0 ? `${n / 1000}k` : n.toLocaleString('en-US')
}

/** 解析上限输入：裸数字，或 `200k` / `1m` 这种紧凑写法（显示用的正是这种形，
 *  输入也就该认它）。解析不出回 null。 */
export function parseTokens(raw: string): number | null {
  const m = /^(\d+)([km]?)$/i.exec(raw.trim())
  if (!m) return null
  const unit = m[2].toLowerCase()
  const n = Number(m[1]) * (unit === 'k' ? 1000 : unit === 'm' ? 1000000 : 1)
  return Number.isFinite(n) ? n : null
}

/**
 * 失焦时该不该写、写什么：**清空 = 清成不限（0）**，不是「没改」；解析不出、负数、
 * 与现值相同都回 null = 不打网络。两件事在这里分清——把清空当没改，人删掉数字
 * 走开之后上限还在，而页面看起来像是删掉了。
 */
export function limitToSave(raw: string, current: number): number | null {
  const s = raw.trim()
  const n = s === '' ? 0 : parseTokens(s)
  if (n === null || n < 0 || n === current) return null
  return n
}

// ── 手动添加模型 ──────────────────────────────────────────────────────────

/** 一次粘一批——逗号、空格、换行都算分隔，去重；已纳管的跳过而不是报错（粘一份
 *  完整清单「把新的加上」是最常见用法）。`dupes` 是跳过的个数，给提示用。 */
export function splitModelNames(
  draft: string,
  existing: ReadonlySet<string>,
): { fresh: string[]; dupes: number } {
  const parsed = Array.from(
    new Set(
      draft
        .split(/[\s,，、]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  )
  const fresh = parsed.filter((m) => !existing.has(m))
  return { fresh, dupes: parsed.length - fresh.length }
}

/** 已纳管的上游模型名集合。 */
export function managedNames(models: readonly ChannelModel[] | null | undefined): Set<string> {
  return new Set((models ?? []).map((m) => m.upstream_model))
}

// ── API 地址 ────────────────────────────────────────────────────────────

/** 草稿合回的 map 与库里那份逐协议比对（两侧都 trim，输入框里的尾随空格不该
 *  触发多余的 PUT）。 */
export function sameBaseURLs(a: BaseURLs, b: BaseURLs): boolean {
  return PROTOCOL_ORDER.every((p) => (a[p] ?? '').trim() === (b[p] ?? '').trim())
}

// ── 凭证池 ───────────────────────────────────────────────────────────────

/** 「正在被用的那一把」按池子顺序取第一把启用的。轮询/随机模式下这只是代表。 */
export function primaryCredential(list: readonly Credential[]): Credential | null {
  return list.find((c) => !c.disabled) ?? null
}

/** 这是启用渠道的最后一把启用凭证：删掉或停掉它都会撞上「能保存的配置一定能启动」
 *  的写后校验。出路是确认后先停渠道再动凭证——校验不放宽，提示给足，但不拦人
 *  （PO 2026-08-28）。订阅渠道例外（#216 豁免闸）：启用中的空订阅渠道本就合法，
 *  先停渠道是多余动作——而且重登只会救活凭证行、不会重新启用渠道，渠道会一直
 *  挂在「停用」上。 */
export function cascadesToChannel(cred: Credential, enabledCount: number, channel: Channel): boolean {
  if (isSubscriptionChannel(channel)) return false
  return !cred.disabled && enabledCount === 1 && !channel.disabled
}

/** 批量粘贴的行：一行一份，空行忽略。 */
export function credentialLines(bulk: string): string[] {
  return bulk
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
}

// ── 订阅渠道的凭证行（#212，DESIGN v0.77）─────────────────────────────

/** 订阅凭证行的读数：账号（email，兜底 sub）· 套餐 · access 过期时刻（unix 秒）。
 *  值解不动（老库里贴了别的、或手改过）不抛——行上还有值本身的显示/复制兜底。 */
export interface SubscriptionSummary {
  account: string
  plan: string
  expiresAt: number
}

/** 订阅渠道判定（*_account，口径层 §2.2 v1.52），与 store.IsSubscriptionCredentialType
 *  同源：两个词一起判是把票面的「*_account」写进代码，Copilot 落地时不用回这里
 *  补。凭证区块、管理弹框、凭证行三处共用，别再手抄字面量。 */
export function isSubscriptionChannel(c: { credential_type?: CredentialType }): boolean {
  return c.credential_type === 'chatgpt_account' || c.credential_type === 'copilot_account'
}

/** 解一份 chatgpt_account 凭证 JSON 整包，取行显示要用的三样。
 *  套餐不在凭证 JSON 的字段表里（§7.13 钉死九字段）：它住在 ID token（兑不到再
 *  退化到 access token）的 JWT claims 里——`https://api.openai.com/auth` 命名空间下
 *  的 `chatgpt_plan_type`，与 magpie 的 siwcPlan 同判。这里只解 base64 摆读数，
 *  不验签——验签是服务端登录链的事，展示层伪造不了任何事实。 */
export function subscriptionSummary(value: string): SubscriptionSummary | null {
  let cred: { email?: string; sub?: string; id_token?: string; access_token?: string; expires_at?: number }
  try {
    cred = JSON.parse(value)
  } catch {
    return null
  }
  if (!cred || typeof cred.expires_at !== 'number') return null
  let plan = ''
  for (const t of [cred.id_token ?? '', cred.access_token ?? '']) {
    plan = jwtClaim(t, ['https://api.openai.com/auth', 'chatgpt_plan_type'])
    if (plan) break
  }
  return {
    account: cred.email || cred.sub || '',
    plan,
    expiresAt: cred.expires_at,
  }
}

/** 从一枚 JWT 的 payload 里取一条按命名空间嵌的字符串 claim；解不动回空串。 */
function jwtClaim(jwt: string, path: string[]): string {
  const part = jwt.split('.')[1]
  if (!part) return ''
  try {
    let node: unknown = JSON.parse(atobUrl(part))
    for (const k of path) {
      if (node == null || typeof node !== 'object') return ''
      node = (node as Record<string, unknown>)[k]
    }
    return typeof node === 'string' ? node : ''
  } catch {
    return ''
  }
}

/** base64url 的 payload 段解回字符串（浏览器 atob 只认标准 base64，补齐对齐与字符表）。 */
function atobUrl(part: string): string {
  const b64 = part.replace(/-/g, '+').replace(/_/g, '/')
  const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4)
  return decodeURIComponent(
    atob(padded)
      .split('')
      .map((c) => '%' + c.charCodeAt(0).toString(16).padStart(2, '0'))
      .join(''),
  )
}

// ── 上游设置表单 ──────────────────────────────────────────────────────────

/** 并发上限输入：空串与非数字都归 0（= 不限）。 */
export function maxConcurrencyOf(raw: string): number {
  const n = Number.parseInt(raw, 10)
  return n > 0 ? n : 0
}

export interface SettingsDraft {
  name: string
  maxConcurrency: number
  provider: string
  authScheme: AuthScheme
  compaction: boolean
  stateful: boolean
}

/** 设置表单有没有未保存的改动。能力位只在露着（声明了 Responses）时参与比较——
 *  没露的那两位不会被提交，也就不算改动。 */
export function settingsDirty(channel: Channel, d: SettingsDraft, showCapabilities: boolean): boolean {
  return (
    d.name !== channel.name ||
    d.maxConcurrency !== channel.max_concurrency ||
    d.provider !== channel.provider ||
    d.authScheme !== channel.auth_scheme ||
    (showCapabilities &&
      (d.compaction !== channel.supports_compaction || d.stateful !== channel.supports_stateful_responses))
  )
}

// ── 额外出站头（#167）──────────────────────────────────────────────────────

/** 额外出站头编辑的一行。 */
export interface HeaderRow {
  name: string
  value: string
}

/** 库里的头集合摆成编辑行，按头名排序（与服务端落库的键序一致）。 */
export function headerRows(h: Record<string, string>): HeaderRow[] {
  return Object.keys(h)
    .sort()
    .map((name) => ({ name, value: h[name] }))
}

/**
 * 编辑行收成要提交的头集合：去首尾空白，整行空的丢掉。只填了一半的行照发——
 * 空头名、空值由服务端 ValidateHeaders 报原文，前端不另写一套闸。唯一在这里拦的是
 * **完全同名**的两行：对象键一合并就静默丢掉一行，服务端根本看不见。
 */
export function headersOf(rows: HeaderRow[]): { headers: Record<string, string>; dup: string } {
  // 无原型：头名填 constructor / __proto__ 也只是普通键，不误报重名、不改原型。
  const headers: Record<string, string> = Object.create(null)
  let dup = ''
  for (const r of rows) {
    const name = r.name.trim()
    const value = r.value.trim()
    if (name === '' && value === '') continue
    if (name in headers && !dup) dup = name
    headers[name] = value
  }
  return { headers, dup }
}

/** 编辑行与库里的头集合是否不同（不看行序与整行空行）。 */
export function headersDirty(saved: Record<string, string>, rows: HeaderRow[]): boolean {
  const { headers, dup } = headersOf(rows)
  if (dup) return true
  const keys = Object.keys(headers)
  return keys.length !== Object.keys(saved).length || keys.some((k) => saved[k] !== headers[k])
}

// ── 渠道预设目录（#181）────────────────────────────────────────────────────

/** 预设的每一套地址：无 plans 时顶层那份就是唯一一套（id 与 name 为空串）。 */
export function presetPlans(p: ChannelPreset): PresetPlan[] {
  return p.plans ?? [{ id: '', name: '', models_dev: p.models_dev, protocols: p.protocols ?? {} }]
}

/** 目录搜索：名、id、任一套地址的**域名**（DESIGN v0.72）。只比 host 不比路径——
 *  搜「anthropic」不该把每家带 /anthropic 兼容端点的厂商都捞出来。空查询原样回。 */
export function filterPresets(list: readonly ChannelPreset[], query: string): ChannelPreset[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...list]
  const host = (u: string) => {
    try {
      return new URL(u).host.toLowerCase()
    } catch {
      return ''
    }
  }
  return list.filter(
    (p) =>
      p.name.toLowerCase().includes(q) ||
      p.id.toLowerCase().includes(q) ||
      presetPlans(p).some((pl) => Object.values(pl.protocols).some((u) => host(u ?? '').includes(q))),
  )
}

/** ModelPicker 的预勾：上游列表 ∩ 建议模型，已纳管的不算（口径层 v1.47）。 */
export function preChecked(
  upstream: Iterable<string>,
  suggested: readonly string[],
  existing: ReadonlySet<string>,
): Set<string> {
  const s = new Set(suggested)
  return new Set([...upstream].filter((n) => s.has(n) && !existing.has(n)))
}

// ── 新建渠道的落库请求体（#216）──────────────────────────────────────────────

/** 新建表单收出一份建渠道请求体：「登录」与「创建」两条路共用，差在后者顺手带
 *  一份 key。字段汇集本身没什么可算的，抽成纯函数为的是把条件随行的三位钉进
 *  测试——尤其 credential_type（订阅组预设才带）：这一项丢了渠道会静默建成
 *  api_key，点「登录」只能吃 400，页面上什么都看不出来。 */
export interface ChannelCreateDraft {
  name: string
  /** joinBaseURLs 合回的原始 map；空串项在这里剔掉（勾了协议但前缀还空着 = 未
   *  声明，服务端 store 同判，剔掉只是不让空键走网络）。 */
  urls: BaseURLs
  maxConcurrency: number
  provider: string
  authScheme: AuthScheme
  /** 能力位只在声明了 Responses 时才进请求体（不传 = 那一列不动）。 */
  compaction: boolean
  stateful: boolean
  /** 订阅组预设的凭证类型；厂商/中转/自定义不给——请求体不带这键，渠道落
   *  DDL 默认 api_key。 */
  credentialType?: CredentialType
}

export interface ChannelCreatePayload {
  name: string
  base_url: BaseURLs
  max_concurrency: number
  provider: string
  auth_scheme: AuthScheme
  supports_compaction?: boolean
  supports_stateful_responses?: boolean
  credential_type?: CredentialType
}

export function channelCreatePayload(d: ChannelCreateDraft): ChannelCreatePayload {
  const base: BaseURLs = {}
  for (const p of PROTOCOL_ORDER) {
    const v = (d.urls[p] ?? '').trim()
    if (v !== '') base[p] = v
  }
  const out: ChannelCreatePayload = {
    name: d.name,
    base_url: base,
    max_concurrency: d.maxConcurrency,
    provider: d.provider,
    auth_scheme: d.authScheme,
  }
  if (declaredProtocols(base).includes('openai_responses')) {
    out.supports_compaction = d.compaction
    // 订阅渠道的有状态续链位恒「否」（服务端同判链紧）：*_account 走转换路径
    // （#204 例外），previous_response_id 无论怎么选都被网关拒——存「是」是撒谎。
    out.supports_stateful_responses = d.credentialType ? false : d.stateful
  }
  if (d.credentialType) out.credential_type = d.credentialType
  return out
}
