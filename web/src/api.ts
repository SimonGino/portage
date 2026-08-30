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

/**
 * 下载一份非 JSON 的响应体。今天只有导出声明文件走它。
 *
 * **不用 `<a href>` 直链**：直链绕开了上面那套 401 处理，会话过期时浏览器会把
 * 登录页那份 HTML 当成 channels.yaml 存下来——一个看着成功的失败。走 fetch 才能
 * 先看状态码再决定存不存。
 */
export async function download(path: string, filename: string): Promise<void> {
  const res = await fetch(BASE + path, { credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    let msg = `请求失败（HTTP ${res.status}）`
    try {
      msg = (JSON.parse(text) as { error?: string }).error ?? msg
    } catch {
      // 非 JSON 的错误体（比如反代吐的 502 页面）用兜底文案。
    }
    if (res.status === 401) onUnauthorized?.()
    throw new ApiError(res.status, msg)
  }
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/**
 * 导入声明文件（#59）：POST 原始 YAML 文本，回变更清单。今天只有导入配置走它——
 * request() 会把 body JSON 化，而这里要发的就是文件原文。
 */
export async function importConfig(yaml: string): Promise<string[]> {
  const res = await fetch(BASE + '/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/yaml' },
    body: yaml,
    credentials: 'same-origin',
  })
  const text = await res.text()
  let payload: unknown = null
  try {
    payload = JSON.parse(text)
  } catch {
    // 非 JSON 的错误体（比如反代吐的 502 页面）用兜底文案。
  }
  if (!res.ok) {
    // 400 里装的是校验原文（一次报全，多行）——写给人看的，原样抛给弹框显示。
    const msg = (payload as { error?: string } | null)?.error ?? `请求失败（HTTP ${res.status}）`
    if (res.status === 401) onUnauthorized?.()
    throw new ApiError(res.status, msg)
  }
  return (payload as { changes: string[] }).changes
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

/** 上游子路径，写在地址行旁边——填协议地址时最容易搞错的就是它。 */
export const PROTOCOL_PATH: Record<Protocol, string> = {
  anthropic: '/v1/messages',
  openai: '/v1/chat/completions',
  openai_responses: '/v1/responses',
}

/**
 * 每协议一份出站根地址（口径层 v0.96 ②）：键是协议名，**填了哪个键就是声明了哪个
 * 协议**，支持协议集由它派生，没有独立的勾选。至少一个键。
 */
export type BaseURLs = Partial<Record<Protocol, string>>

/** BaseURLs 的固定遍历序，同时是转换时的回退优先级（cc > responses > anthropic）。 */
export const PROTOCOL_ORDER: Protocol[] = ['openai', 'openai_responses', 'anthropic']

/** 已声明的协议，按回退序。 */
export function declaredProtocols(urls: BaseURLs): Protocol[] {
  return PROTOCOL_ORDER.filter((p) => (urls[p] ?? '').trim() !== '')
}

/** 第一份已填的地址：搜索、厂商图标这类「拿一个代表值」的地方用它。 */
export function firstBaseURL(urls: BaseURLs): string {
  const p = declaredProtocols(urls)[0]
  return p ? urls[p]! : ''
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
 * 一格检测的三态结论（口径层 v0.43 立，v0.96 起检测只剩模型级这一层）。
 *
 * 刻意不是二态：把 429 画成「不通」、把 400 画成「通」、把超时画成「不存在」都是
 * 撒谎，而检测的口径是只提示——提示就得诚实。「说不清」摆出状态码，判断留给人。
 */
export type ProbeState = 'ok' | 'missing' | 'unclear'

export interface ModelProbeResult {
  protocol: Protocol
  /** ok=2xx 真回了话、missing=404/405、unclear=其余（含没拿到响应，status 为 0）。 */
  state: ProbeState
  status: number
  detail: string
}

/** 一个纳管模型的检测结论行：勾选的协议每一侧一格。 */
export interface ModelProbeRow {
  model: string
  results: ModelProbeResult[]
}

/**
 * 一次检测的完整回包（口径层 v0.96）：模型 × 协议矩阵 + 用的哪把凭证。
 * 只提示，不落库、不参与路由；结果只活在弹层里，关弹层即失。
 */
export interface ChannelProbe {
  /** 检测用的那把凭证的名字。403 的格子要靠它说清「用的是哪把」。 */
  credential: string
  models: ModelProbeRow[]
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
  /**
   * 输入上限（估算）（口径层 v0.99）：0 = 不限。判据是入站原始请求体字节数 ÷ 4，
   * 界面文案必须带「估算」，不承诺精确。
   */
  max_input_tokens: number
  /**
   * 四价（口径层 §2.10，#74），USD/百万 token。**null = 未定价，0 = 真免费**，
   * 两态不能抹成一个：未定价且有用量要提醒，真免费什么都不用提。
   */
  price_input: number | null
  price_output: number | null
  price_cache_read: number | null
  price_cache_write: number | null
  /**
   * 这条条目名下有没有报过 usage 的流水（渠道名 × 上游模型名）。未定价提醒的判据
   * 是「四价全 null 且它为 true」——没人用过的条目不催着定价。
   */
  has_usage: boolean
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
   * 所以协议不出现在对外模型名里。v0.96 起是**派生值**：由 base_url 里填了哪些键
   * 推导，后端照旧发下来省得每处自己算。
   */
  protocols: Protocol[]
  /** 每协议出站根地址（v0.96）：填了即声明该协议。写入时整个对象一起提交。 */
  base_url: BaseURLs
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
  /**
   * models.dev 的 provider id 标注（口径层 §2.10，#74）：只服务填价建议与图标分组，
   * 不参与路由，取值不校验。空串 = 未标注。
   */
  provider: string
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
  /** 停用现场：为什么、何时停的。停用与恢复都只人工做（v0.95 去掉 401 自动摘除）。 */
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
  /** 明文（v0.47）。空串有两个意思，靠 `mine` 分辨（#73）：mine 为真时 =
   *  **原值没存过**——这把是加 key_plain 那一列之前建的，库里只剩哈希，还原不了，
   *  只能删了重建；mine 为假时 = **他人的 key**，明文仅 key 主人可见，后端不下发。 */
  key: string
  allowed_models: string
  disabled: boolean
  created_at: string
  /** 归属用户 id（#63/#66）。null = 无主 key（声明文件所建、认领前的存量）。 */
  user_id: number | null
  /** 归属用户的邮箱，归属列直接显示它；无主为空串。 */
  owner: string
  /** 这把 key 的明文与编辑对当前登录者开不开放：自己的 key，或（admin 视角的）
   *  无主 key。为假时只剩元数据治理——停用与删除。 */
  mine: boolean
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
  /**
   * 并发闸排队耗时（口径层 v0.52，露出见 #7）。不可空：写侧每行必落，「没排队」
   * 与「排了 0ms」在这一列上同档，一律 0。所以 > 0 才是「真排过队」，详情框
   * 只在那时摆这一格——照挂在 0 上会让人读出「这次排了 0ms」，而绝大多数行的
   * 实情是压根没进闸。
   */
  queue_wait_ms: number
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
  /**
   * 这一次的成本（口径层 §2.10，#65/#74），USD，落库时点算死、改价不追溯。
   *
   * 三态：null = 无用量可计（没走到上游 / 上游没报 usage / #74 之前的老流水）；
   * 0 = 有用量但未定价或真免费；正数 = 算出来的钱。null 与 0 不能抹成一个。
   */
  cost: number | null
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

/** models.dev 的一家 provider（`GET /pricing/providers`，#74），按名字排好序发来。 */
export interface PricingProvider {
  id: string
  name: string
}

/**
 * models.dev 快照里一个模型的四价（USD/百万 token）。字段缺席 = 快照里没这一价，
 * 建议时按「没有建议」处理，别补 0。
 */
export interface PricingModelPrice {
  input?: number
  output?: number
  cache_read?: number
  cache_write?: number
}

/** `GET /pricing/models?provider=` 的回包。查无此家 models 是空对象，不报错。 */
export interface PricingModels {
  provider: string
  models: Record<string, PricingModelPrice>
}

/** 一个用户（internal/store/user.go 的 User）。永不带密码哈希。 */
export interface User {
  id: number
  email: string
  display_name: string
  role: 'admin' | 'user'
  disabled: boolean
  email_verified: boolean
  /** false = OAuth-only 账号：设密码走「忘记密码」的邮件链路。 */
  has_password: boolean
  created_at: string
}

export interface SessionState {
  authenticated: boolean
  password_set: boolean
  /** #72 起登着时带出「我是谁」：按角色分壳、按验证态锁功能都靠它。 */
  user?: User
}

/** 登录页「有哪些门」：注册开不开、OAuth 有哪几家（GET /auth-config，不鉴权）。 */
export interface AuthConfig {
  registration_open: boolean
  oauth: string[]
  registration_closed_reason?: string
}

export interface InviteCode {
  id: number
  code: string
  /** unix 秒；null = 不过期。 */
  expires_at: number | null
  /** 空串 = 还没人用。 */
  used_by_email: string
  used_at: string
  created_at: string
}

/** 账号绑定的一个上游身份。不带上游 id——那串数字对人没有意义。 */
export interface OAuthIdentity {
  provider: string
  created_at: string
}

/**
 * 登录与邮件配置（GET /auth-settings）。secret 三样（SMTP 密码、两家 client_secret）
 * **只回「设没设」，永不回值**——同「上游 key 只存服务端」口径。
 */
export interface AuthSettings {
  site_url: string
  smtp: {
    host: string
    port: string
    encryption: string
    username: string
    from: string
    password_set: boolean
  }
  github: { client_id: string; secret_set: boolean }
  google: { client_id: string; secret_set: boolean }
  callback_urls: { github: string; google: string }
}
