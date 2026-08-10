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

export type Protocol = 'anthropic' | 'openai_cc' | 'openai_responses'

export const PROTOCOL_LABEL: Record<Protocol, string> = {
  anthropic: 'Anthropic',
  openai_cc: 'OpenAI Chat Completions',
  openai_responses: 'OpenAI Responses',
}

/** 卡片上一行放三个全称太挤，列表处用短名。 */
export const PROTOCOL_SHORT: Record<Protocol, string> = {
  anthropic: 'Anthropic',
  openai_cc: 'CC',
  openai_responses: 'Responses',
}

/** 上游子路径，写在协议勾选框旁边——填 base_url 时最容易搞错的就是它。 */
export const PROTOCOL_PATH: Record<Protocol, string> = {
  anthropic: '/v1/messages',
  openai_cc: '/v1/chat/completions',
  openai_responses: '/v1/responses',
}

/** 一次协议可达性探测的结果。只提示，不落库、不参与路由（口径层 v0.33）。 */
export interface ProbeResult {
  protocol: Protocol
  reachable: boolean
  status: number
  detail: string
}

export interface ChannelModel {
  id: number
  upstream_model: string
  disabled: boolean
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
  key_mode: string
  disabled: boolean
  /** 只有这个布尔，没有凭证本身：上游凭证只写不回读（PO 于 M3 裁定）。 */
  has_credential: boolean
  models: ChannelModel[] | null
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
  allowed_models: string
  disabled: boolean
  created_at: string
}

export interface CallLog {
  id: number
  created_at: string
  api_key_name: string
  client_protocol: string
  upstream_protocol: string
  model_requested: string
  model_upstream: string
  channel_name: string
  status: number
  retry_count: number
  ttft_ms: number | null
  total_ms: number
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  error: string
}

export interface UsageRow {
  model_requested: string
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
