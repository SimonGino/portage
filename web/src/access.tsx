/**
 * 接入指引（DESIGN v0.73，#182；口径层 v1.48）：两空间共用的一个组件，把「这把 key
 * 怎么接进客户端」拼成一段可复制的片段。只拼装已有事实，不建任何对象——协议 × 片段
 * 两个分段、key 与模型两个下拉，外加一段等宽代码框。
 *
 * 片段内容以 README 的两段 harness 配置为准（Claude Code / Codex），curl / Python /
 * Node 三段从同一批事实（端点、key、模型）展开。
 */

import { useEffect, useState } from 'react'
import { api } from './api'
import type { ApiKey, SessionState } from './api'
import { CopyButton, useList } from './ui'
import { Picker, Segmented } from './fields'
import type { Option } from './fields'
import { ModelIcon } from './icons'

/** 协议三档：Chat（/v1/chat/completions）/ Responses / Anthropic。 */
export type Proto = 'chat' | 'responses' | 'anthropic'

/** 片段五档：curl / Python / Node 两个官方 SDK / Claude Code / Codex CLI。 */
export type SnippetKind = 'curl' | 'python' | 'node' | 'claude-code' | 'codex'

/** 模型目录 / 下拉共用的一行（#189）：接入指引只关心 Codex 要的输入上限。 */
export interface GuideModel {
  id: string
  direct: boolean
  max_input_tokens: number
  snapshot_context: number
}

/** harness 档锁协议（口径层 v1.48）：Claude Code 只说 Anthropic Messages、Codex 只说
 *  Responses。返回 null 表示这档不锁，协议行可自由切换。 */
export function lockedProtocol(kind: SnippetKind): Proto | null {
  if (kind === 'claude-code') return 'anthropic'
  if (kind === 'codex') return 'responses'
  return null
}

/** 各协议对外的完整端点。Claude Code 给根、Codex 给 /v1（与 README 同源），其余按协议。 */
export function endpointOf(base: string, proto: Proto, kind: SnippetKind): string {
  if (kind === 'claude-code') return base
  if (kind === 'codex') return base + '/v1'
  if (proto === 'chat') return base + '/v1/chat/completions'
  if (proto === 'responses') return base + '/v1/responses'
  return base + '/v1/messages'
}

/** Codex 的 model_context_window：条目已设的输入上限（估算）优先，快照建议次之，
 *  都没有兜 200000 并注明「按真实上限改」。返回 [值, 注释]。 */
export function contextWindow(m: GuideModel | null): [number, string] {
  if (m && m.max_input_tokens > 0) return [m.max_input_tokens, '选中模型的输入上限（估算）']
  if (m && m.snapshot_context > 0) return [m.snapshot_context, 'models.dev 快照值，按真实上限改']
  return [200000, '取不到上限的兜底，按真实上限改']
}

export interface SnippetFacts {
  base: string
  key: string
  model: string
  context: number
  contextNote: string
}

/** 片段正文。等宽展示不高亮，靠缩进与行内注释自己说清自己。 */
export function buildSnippet(proto: Proto, kind: SnippetKind, f: SnippetFacts): string {
  switch (kind) {
    case 'claude-code':
      return [
        `export ANTHROPIC_BASE_URL=${f.base}`,
        `export ANTHROPIC_AUTH_TOKEN=${f.key}    # Portage 的 key，不是上游厂商 key`,
        `export ANTHROPIC_MODEL=${f.model}        # 接入点名，或 渠道/模型 限定名`,
        `export ANTHROPIC_DEFAULT_HAIKU_MODEL=${f.model}  # 后台小模型也走网关；老版本叫 ANTHROPIC_SMALL_FAST_MODEL`,
        'claude',
      ].join('\n')
    case 'codex':
      return [
        'model_provider = "portage"',
        '',
        '[model_providers.portage]',
        'name = "Portage"',
        `base_url = "${f.base}/v1"`,
        'wire_api = "responses"          # 出站转不转译由网关按渠道决定',
        'env_key = "PORTAGE_API_KEY"     # 你的 Portage key，不是上游 key',
        '',
        '[profiles.gw]',
        `model = "${f.model}"`,
        'model_provider = "portage"',
        `model_context_window = ${f.context}   # ${f.contextNote}`,
      ].join('\n')
    case 'python':
      if (proto === 'anthropic') {
        return [
          'from anthropic import Anthropic',
          '',
          `client = Anthropic(base_url="${f.base}", api_key="${f.key}")`,
          '',
          'msg = client.messages.create(',
          `    model="${f.model}",`,
          '    max_tokens=1024,',
          '    messages=[{"role": "user", "content": "Hi"}],',
          ')',
          'print(msg.content[0].text)',
        ].join('\n')
      }
      if (proto === 'responses') {
        return [
          'from openai import OpenAI',
          '',
          `client = OpenAI(base_url="${f.base}/v1", api_key="${f.key}")`,
          '',
          'resp = client.responses.create(',
          `    model="${f.model}",`,
          '    input="Hi",',
          ')',
          'print(resp.output_text)',
        ].join('\n')
      }
      return [
        'from openai import OpenAI',
        '',
        `client = OpenAI(base_url="${f.base}/v1", api_key="${f.key}")`,
        '',
        'resp = client.chat.completions.create(',
        `    model="${f.model}",`,
        '    messages=[{"role": "user", "content": "Hi"}],',
        ')',
        'print(resp.choices[0].message.content)',
      ].join('\n')
    case 'node':
      if (proto === 'anthropic') {
        return [
          'import Anthropic from "@anthropic-ai/sdk"',
          '',
          `const client = new Anthropic({ baseURL: "${f.base}", apiKey: "${f.key}" });`,
          '',
          'const msg = await client.messages.create({',
          `  model: "${f.model}",`,
          '  max_tokens: 1024,',
          '  messages: [{ role: "user", content: "Hi" }],',
          '});',
          'console.log(msg.content[0].text);',
        ].join('\n')
      }
      if (proto === 'responses') {
        return [
          'import OpenAI from "openai"',
          '',
          `const client = new OpenAI({ baseURL: "${f.base}/v1", apiKey: "${f.key}" });`,
          '',
          'const resp = await client.responses.create({',
          `  model: "${f.model}",`,
          '  input: "Hi",',
          '});',
          'console.log(resp.output_text);',
        ].join('\n')
      }
      return [
        'import OpenAI from "openai"',
        '',
        `const client = new OpenAI({ baseURL: "${f.base}/v1", apiKey: "${f.key}" });`,
        '',
        'const resp = await client.chat.completions.create({',
        `  model: "${f.model}",`,
        '  messages: [{ role: "user", content: "Hi" }],',
        '});',
        'console.log(resp.choices[0].message.content);',
      ].join('\n')
    default: // curl
      if (proto === 'anthropic') {
        return [
          `curl ${f.base}/v1/messages \\`,
          `  -H "x-api-key: ${f.key}" \\`,
          '  -H "anthropic-version: 2023-06-01" \\',
          '  -H "Content-Type: application/json" \\',
          `  -d '{"model": "${f.model}", "max_tokens": 1024, "messages": [{"role": "user", "content": "Hi"}]}'`,
        ].join('\n')
      }
      const path = proto === 'responses' ? '/v1/responses' : '/v1/chat/completions'
      const body =
        proto === 'responses'
          ? `{"model": "${f.model}", "input": "Hi"}`
          : `{"model": "${f.model}", "messages": [{"role": "user", "content": "Hi"}]}`
      return [
        `curl ${f.base}${path} \\`,
        `  -H "Authorization: Bearer ${f.key}" \\`,
        '  -H "Content-Type: application/json" \\',
        `  -d '${body}'`,
      ].join('\n')
  }
}

/** key 下拉的可见集（#182，口径层 v1.48）：只列本人的启用中 key（mine 标志在管理侧
 *  已把无主 key 算进来，带「无主」副行）；他人的 key 不出现（#73 明文不下发），
 *  停用的不列。无明文的照列——禁选与否交 Picker 的 disabled。 */
export function selectableKeys(keys: ApiKey[]): ApiKey[] {
  return keys.filter((k) => !k.disabled && k.mine)
}

/** 模型下拉按选中 key 的白名单过滤：空/`*` = 全部（后端把空白名单当 `*`）。 */
export function whitelisted(models: GuideModel[], allowed: string): GuideModel[] {
  if (allowed === '*' || allowed.trim() === '') return models
  const set = allowed.split(',')
  return models.filter((m) => set.includes(m.id))
}

const PROTO_OPTIONS: Option<Proto>[] = [
  { value: 'chat', label: 'Chat' },
  { value: 'responses', label: 'Responses' },
  { value: 'anthropic', label: 'Anthropic' },
]

const KIND_OPTIONS: Option<SnippetKind>[] = [
  { value: 'curl', label: 'curl' },
  { value: 'python', label: 'Python' },
  { value: 'node', label: 'Node' },
  { value: 'claude-code', label: 'Claude Code' },
  { value: 'codex', label: 'Codex CLI' },
]

/**
 * 两空间共用的接入指引。`keys` 传页面已有的列表（管理侧 /keys、我的侧 /my/keys），
 * `models` 传模型目录的行——网关页上目录就在下面，同一份数据两处用。
 */
export function AccessGuide({
  keys,
  models,
  space,
  model,
  onModel,
  onCreateKey,
}: {
  keys: ApiKey[]
  models: GuideModel[]
  space: 'admin' | 'my'
  /** 受控的选中模型：管理侧模型目录点行带入。 */
  model: string | null
  onModel: (m: string) => void
  onCreateKey: () => void
}) {
  const [proto, setProto] = useState<Proto>('chat')
  const [kind, setKind] = useState<SnippetKind>('curl')
  const [keyID, setKeyID] = useState<number | null>(null)
  // 站点外部 URL（#182 订正：不加 public_url）：设置里填了显示它，没填退回面板 origin。
  const session = useList(() => api.get<SessionState>('/session'))

  // key 下拉只列本人的启用中 key（管理侧另含无主 key——mine 标志已一并算好）；
  // 他人明文本就不下发（#73），这里再滤一道 mine，停用的不列。
  const listed = selectableKeys(keys)
  const keyOptions: Option<number>[] = listed.map((k) => ({
    value: k.id,
    label: k.name,
    hint: k.user_id === null ? '无主' : undefined,
    disabled: !k.key,
    keywords: k.key ? undefined : '无明文，新建一把再用',
  }))
  // 默认选第一把有明文的；无明文的旧 key 列出但禁选（副行「无明文，新建一把再用」）。
  const selected = listed.find((k) => k.id === keyID) ?? null
  const firstPlain = listed.find((k) => k.key)
  useEffect(() => {
    if (keyID === null && firstPlain) setKeyID(firstPlain.id)
  }, [keyID, firstPlain])

  // 模型按选中 key 的白名单过滤（空 = 全部）；目录顺序已是接入点在前。
  const routable = selected ? whitelisted(models, selected.allowed_models) : models
  const modelOptions: Option<string>[] = routable.map((m) => ({
    value: m.id,
    label: m.id,
    group: m.direct ? '直连（渠道/模型）' : '接入点',
    icon: <ModelIcon model={m.id} size={16} />,
  }))
  const selectedModel = routable.find((m) => m.id === model) ?? null
  // 目录点行带入的名字可能被换 key 后的白名单筛掉：下拉显示空、片段用占位。
  const modelShown = selectedModel ? model : null

  const siteURL = (session.data?.site_url ?? '').replace(/\/+$/, '')
  const base = siteURL || window.location.origin
  const locked = lockedProtocol(kind)
  const effProto = locked ?? proto
  const [ctx, ctxNote] = contextWindow(selectedModel)
  const snippet = buildSnippet(effProto, kind, {
    base,
    key: selected?.key || 'sk-ptg-…',
    model: modelShown || 'your-model',
    context: ctx,
    contextNote: ctxNote,
  })

  return (
    <section className="section access-guide">
      <header className="section-head">
        <h2>接入</h2>
      </header>
      <div className="access-rows">
        <div className="access-row">
          <span className="access-label">协议</span>
          <div className={'access-seg' + (locked ? ' is-locked' : '')}>
            <Segmented value={effProto} options={PROTO_OPTIONS} onChange={setProto} />
          </div>
          <span className="access-endpoint">
            <code>{endpointOf(base, effProto, kind)}</code>
          </span>
        </div>
        <div className="access-row">
          <span className="access-label">片段</span>
          <Segmented value={kind} options={KIND_OPTIONS} onChange={setKind} />
        </div>
        <div className="access-row">
          <span className="access-label">API Key</span>
          {listed.length === 0 ? (
            <button type="button" className="btn btn-primary" onClick={onCreateKey}>
              新建一把
            </button>
          ) : (
            <Picker<number>
              value={selected?.id ?? null}
              options={keyOptions}
              onChange={setKeyID}
              placeholder={selected?.key ? undefined : '选一把有明文的 key'}
            />
          )}
          {/* 无明文的旧 key 在下拉里禁选；全都没有明文时把原因摆到行上来，别让人猜。 */}
          {listed.length > 0 && !firstPlain && (
            <span className="muted">无明文，新建一把再用</span>
          )}
        </div>
        <div className="access-row">
          <span className="access-label">模型</span>
          {models.length === 0 ? (
            <span className="muted">管理员还没配好渠道或接入点</span>
          ) : routable.length === 0 ? (
            <span className="muted">这把 key 的白名单不含任何可路由模型</span>
          ) : (
            <Picker<string>
              value={modelShown}
              options={modelOptions}
              onChange={onModel}
              placeholder="选模型…"
            />
          )}
        </div>
      </div>
      {!siteURL && (
        <p className="muted access-origin-note">反代部署请在设置里填站点外部 URL</p>
      )}
      <div className="snippet-box">
        <span className="snippet-copy">
          <CopyButton value={snippet} />
        </span>
        <pre className="snippet">{snippet}</pre>
      </div>
      {space === 'my' && (
        <p className="muted">key 只列你自己的；白名单以选中 key 为准，片段里的值都是真实的。</p>
      )}
    </section>
  )
}
