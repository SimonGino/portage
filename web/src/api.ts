// 管理端 API 的唯一出入口。所有请求都从这里走，好处是 401 只需要在一个地方处理。

const BASE = '/admin/api'

/** ApiError 带着状态码，调用方靠它区分「配置被校验挡了」（400）和「掉线了」（401）。 */
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

// 掉线的统一处理：后端 401 之后，页面上任何一次请求都会走到这里，
// 由 App 订阅这个回调把界面切回登录页。不用抛异常层层上传，是因为每个页面
// 都写一遍「如果是 401 就跳登录」既啰嗦又容易漏。
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    // 会话是 cookie。同源下 fetch 默认就带，写出来是为了别被将来某次
    // 「顺手改成跨域」悄悄破坏。
    credentials: 'same-origin',
  })

  // 204：写接口成功但没有回值，body 是空的，别去 json() 它。
  if (res.status === 204) return undefined as T

  let payload: unknown = null
  const text = await res.text()
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!res.ok) {
    // 后端的错误一律是 {"error": "..."}，且 400 里装的是校验原文——
    // 那段话是写给人看的，直接显示，不要包装成「保存失败」。
    const msg =
      (payload as { error?: string } | null)?.error ?? `请求失败（HTTP ${res.status}）`
    if (res.status === 401 && !path.startsWith('/login')) onUnauthorized?.()
    throw new ApiError(res.status, msg)
  }
  return payload as T
}

export const api = {
  get: <T,>(path: string) => request<T>('GET', path),
  post: <T,>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T,>(path: string, body?: unknown) => request<T>('PUT', path, body ?? {}),
  del: <T,>(path: string) => request<T>('DELETE', path),
}

// ── 与后端结构一一对应的类型 ────────────────────────────────────────────
// 字段名跟 internal/store/admin.go 的 json tag 对齐，改那边记得改这里。

export type Protocol = 'anthropic' | 'openai' | 'openai_responses'

export const PROTOCOL_LABEL: Record<Protocol, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  openai_responses: 'OpenAI-Responses',
}

/** 卡片上一行放三个全称太挤，列表处用短名。 */
export const PROTOCOL_SHORT: Record<Protocol, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  openai_responses: 'Responses',
}

/** 上游子路径，写在协议勾选框旁边——填 base_url 时最容易搞错的就是它。 */
export const PROTOCOL_PATH: Record<Protocol, string> = {
  anthropic: '/v1/messages',
  openai: '/v1/chat/completions',
  openai_responses: '/v1/responses',
}

/**
 * 路线图里有、但网关还说不了的协议（口径层 v0.36）。
 *
 * 摆出来是为了回答「这里为什么没有 Gemini」，不是为了让人选中——后端 ParseSet 根本
 * 不认这个取值，能选中就等于能建出一个每次请求都失败的渠道。
 */
export const PROTOCOL_SOON: { value: string; label: string; hint: string }[] = [
  { value: 'gemini', label: 'Gemini', hint: '暂未支持' },
]

/**
 * 一格探测的三态结论。子路径层与模型级共用（口径层 v0.43 立，v0.89 子路径层跟上）。
 *
 * 刻意不是二态：把 429 画成「不通」、把 400 画成「通」、把超时画成「不存在」都是
 * 撒谎，而探测的口径是只提示——提示就得诚实。「说不清」摆出状态码，判断留给人。
 */
export type ProbeState = 'ok' | 'missing' | 'unclear'

/** 一次协议可达性探测的结果。只提示，不落库、不参与路由（口径层 v0.33）。 */
export interface ProbeResult {
  protocol: Protocol
  /** ok=子路径存在、missing=404/405、unclear=没拿到响应。 */
  state: ProbeState
  status: number
  detail: string
}

/**
 * 探测结果按凭证分组（口径层 v0.38 逐把凭证探，含已停用的）。
 *
 * 含停用的那些是有意的：恢复只人工做，「这把被摘的凭证现在还坏不坏」除了删掉重配
 * 就没有别的办法回答。一份凭证都没有时后端仍回一组，credential 是空串。
 */
export interface ProbeGroup {
  credential: string
  disabled: boolean
  results: ProbeResult[]
}

export interface ModelProbeResult {
  protocol: Protocol
  state: ProbeState
  status: number
  detail: string
}

/** 一个纳管模型的探测结论行：它的有效协议集里每一侧一格。 */
export interface ModelProbeRow {
  model: string
  results: ModelProbeResult[]
}

/** 一次「探测协议」的完整回包：子路径层（逐凭证）+ 模型矩阵（第一把启用凭证）。 */
export interface ChannelProbe {
  credentials: ProbeGroup[]
  models: ModelProbeRow[] | null
  /** 模型矩阵用的那把凭证的名字。403 的格子要靠它说清「探的是哪把」。 */
  model_credential: string
}

export interface ChannelModel {
  id: number
  upstream_model: string
  /**
   * 这个模型自己能走的协议子集（口径层 v0.40）。**空数组 = 继承渠道全集**，绝大多数
   * 模型都该是空的；只有「渠道会说 anthropic，但这个模型不在 /v1/messages 上」这种
   * 例外才填。路由时与渠道集取交集，没有交集这个模型就当下用不了。
   */
  protocols: Protocol[]
  disabled: boolean
}

/**
 * 朝一个协议侧拉上游模型列表的结果（口径层 v0.40）。
 *
 * **只做填表助手**：拉回来的东西不落库、不参与路由，人在表单上确认之后落库的才是配置。
 * 中转站返回一份写死的大列表是常态，直接采信等于把一份会撒谎的缓存放进请求路径。
 */
export interface ModelListResult {
  /** 这份列表适用的协议。openai 与 openai_responses 共用一次拉取，所以是数组。 */
  protocols: Protocol[]
  models: string[] | null
  status: number
  detail: string
}

export interface Channel {
  id: number
  name: string
  /**
   * 这个渠道能说的上游协议集（口径层 v0.33）。选哪个由入站端点定——能透传就透传，
   * 所以协议不出现在对外模型名里。
   */
  protocols: Protocol[]
  base_url: string
  /** 凭证选取模式（口径层 v0.11）：轮询或随机。 */
  key_mode: KeyMode
  /** 渠道级并发上限（口径层 v0.49）：0 = 不限。 */
  max_concurrency: number
  /**
   * 这个渠道的上游认不认 Codex 的 `compaction_trigger`（口径层 v0.54）：默认否。
   * 为否时 Responses 透传对压缩 turn 明确拒绝——Responses 形状的 wire 不等于支持
   * 压缩，原样透传只会让 Codex 收到 0 个 compaction item 然后 Fatal。
   */
  supports_compaction: boolean
  /**
   * 这个渠道的上游认不认 Responses 的有状态语义 `previous_response_id`（口径层
   * v0.88）：**默认是**，与上一位相反。为否时 Responses 透传对带 previous_response_id
   * 的请求回 400（code `previous_response_not_found`，客户端照它重发完整 input）。
   * 默认取是是因为代价不对称的方向反过来了：配错成是，上游自己会回一句明确的
   * not_found；配错成否，会打断一条本来正常工作的续链，而那种打断在页面上看不出来。
   */
  supports_stateful_responses: boolean
  disabled: boolean
  /**
   * 可用/停用凭证计数（口径层 v0.38）。这里只有计数，没有凭证值——值由凭证池那一个
   * 接口单独发（v0.47）。
   * 用计数而不是「有没有」：摘光不设特例，3 把里坏了 2 把时布尔显示的仍是「有凭证」，
   * 把最该被看见的劣化过程整个藏住。
   */
  enabled_keys: number
  disabled_keys: number
  models: ChannelModel[] | null
}

export type KeyMode = 'polling' | 'random'

export const KEY_MODE_OPTIONS: { value: KeyMode; label: string; hint: string }[] = [
  { value: 'polling', label: '轮询', hint: '依次轮转' },
  { value: 'random', label: '随机', hint: '每次随机挑' },
]

/**
 * 凭证池里的一份凭证（口径层 v0.38）。
 *
 * 带值（v0.47 推翻 v0.28 的「只写不回读」）：PO 裁定页面上要能看能复制，否则「这把
 * 到底是哪一把」没有直观表达。掩码在页面上做，服务端发的是全串。名字仍然是归因依据
 * ——日志与用量按名字认凭证。
 */
export interface Credential {
  id: number
  name: string
  /** 明文的上游 key。 */
  credential: string
  disabled: boolean
  /** 401 摘除的现场：只有 401 会自动摘，且只人工恢复。 */
  disabled_reason: string
  disabled_at: string
  created_at: string
}

export interface Candidate {
  id: number
  channel_model_id: number
  channel_id: number
  channel_name: string
  upstream_model: string
  weight: number
}

export interface AccessPoint {
  id: number
  model: string
  disabled: boolean
  candidates: Candidate[] | null
}

export interface ApiKey {
  id: number
  name: string
  /** 明文（v0.47）。**空串 = 原值没存过**——这把是加 key_plain 那一列之前建的，
   *  库里只剩哈希，还原不了，只能删了重建。不是「key 是空的」。 */
  key: string
  allowed_models: string
  disabled: boolean
  created_at: string
}

export interface CallLog {
  id: number
  created_at: string
  api_key_name: string
  /**
   * 这次打的转发端点路径（#17），取值是那四条之一：`/v1/messages`、
   * `/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`。
   *
   * 它不能由 client_protocol 推出来：前两条的入站协议同为 anthropic。空串 = 加列
   * 之前的老流水（新行一律有值，鉴权失败、限流那些也有），不是「没打端点」。
   */
  endpoint: string
  /**
   * 这次**打到上游**的那条路径（#20），与 `endpoint` 成对。跨协议时两者不等（A 入口
   * 落 openai 渠道，出站是 `/v1/chat/completions`），同协议透传时相等。
   *
   * 空串有两个意思，靠 `endpoint` 分辨：两者皆空 = 加列之前的老流水；`endpoint` 有值
   * 而它空 = 这次请求**从没发到上游**（401、429、count_tokens 撞非 anthropic 渠道的
   * 501、并发闸队满）。`upstream_protocol` 推不出它——count_tokens 没有出口对应物。
   */
  upstream_endpoint: string
  client_protocol: string
  upstream_protocol: string
  model_requested: string
  model_upstream: string
  channel_name: string
  /** 本次真正发请求的那份凭证名（换过则是最后一份）。没走到上游时是空串。 */
  channel_key_name: string
  status: number
  retry_count: number
  /** 同步(false)/流式(true)。null = 不知道：没解析到请求体的行（鉴权失败那类）与老流水。 */
  is_stream: boolean | null
  ttft_ms: number | null
  total_ms: number
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  /**
   * 思考 token（口径层 v0.66）。是 output_tokens 的**明细**不是另一笔，别相加。
   *
   * 三态：null = 上游不报这个数（Anthropic 一路，以及 v0.66 之前的老流水）；
   * 0 = 上游报了、这次没思考；正数 = 这次思考花了这么多。null 与 0 不能抹成一个。
   */
  reasoning_tokens: number | null
  /** 网关自己的固定词表（upstream_error / queue_full / stream_aborted…），可枚举。 */
  error: string
  /**
   * 上游错误原文的前 2KB（口径层 v0.53）。
   *
   * 三态：null = 没存过；空串 = 上游回了错但响应体本身是空的（这也是排障信息）；
   * 有文本 = 上游原文。别把 null 与空串当同一件事。
   *
   * 与 error 不同步出现：上游透传 4xx 的 error 是空的（透传成功不算网关侧错误），
   * 这一列却有值——所以详情里这一段的判据是 status >= 400，不是 error 非空。
   */
  error_detail: string | null
  /**
   * 上游响应头 request-id 的原样快照（口径层 v0.56），拿它去上游那边对账。
   *
   * 与 error_detail 有两点不同：**成功行也有**（对账的常见场景正是「用量对不上，
   * 想问上游这次到底计了多少」），且**不可空**——空串一档吃掉三种情况（没走到
   * 上游、上游没回这个头、v0.56 之前的老流水），它们在对账上是同一件事：没有
   * 可用的 id。故前端只判空串，不判 null。
   */
  upstream_request_id: string
}

/** 用量汇总的一行。label 按聚合维度取值：模型名 / 网关 key 名 / 上游凭证名。 */
export interface UsageRow {
  label: string
  calls: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
}

/**
 * 节律带上的一格（`GET /usage/buckets`，#21）。
 *
 * `bucket` 是**本地时区**的桶起点，`unit=day` 为 `YYYY-MM-DD`、`unit=hour` 为
 * `YYYY-MM-DD HH:00`——不是 ISO 串，别拿 `new Date()` 去解析它，前端按本地时间
 * 自己铺格子再拿这个键去查（见 pages/rankings/intervals.ts）。
 *
 * 后端只回**已经发生**的桶：空桶补零，未到的区间一个都不给（口径层 v0.86 ③）。
 */
export interface BucketUsage {
  bucket: string
  calls: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
}

export interface SessionState {
  authenticated: boolean
  password_set: boolean
}
