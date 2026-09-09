// 模型页的纯推导。**这个文件不 import React**：清单排序、协议子集归一、
// 「证据不全不推断」、上限与模型名的解析——这些是「算错了且看不出来」的一类，
// 抽成纯函数才测得动（derive.test.ts；照排行页 intervals.ts 成例，#56）。
// 状态与网络在 useChannel.tsx，JSX 只负责摆。

import { PROTOCOL_ORDER } from '../../api'
import type {
  AuthScheme,
  BaseURLs,
  Channel,
  ChannelModel,
  Credential,
  ModelListResult,
  Protocol,
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
 *  （PO 2026-08-28）。 */
export function cascadesToChannel(cred: Credential, enabledCount: number, channel: Channel): boolean {
  return !cred.disabled && enabledCount === 1 && !channel.disabled
}

/** 批量粘贴的行：一行一份，空行忽略。 */
export function credentialLines(bulk: string): string[] {
  return bulk
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
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
