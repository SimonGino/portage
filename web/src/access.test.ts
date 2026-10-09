/** 接入指引纯函数层（#182/#195）：锁定矩阵、端点、Codex 窗口、片段生成、下拉过滤。 */

import { describe, expect, it } from 'vitest'
import {
  buildSnippet,
  contextWindow,
  endpointOf,
  lockedProtocol,
  selectableKeys,
  whitelisted,
  type GuideModel,
} from './access'
import type { ApiKey } from './api'

const key = (over: Partial<ApiKey>): ApiKey => ({
  id: 1,
  name: 'k',
  key: 'sk-ptg-plain',
  allowed_models: '*',
  disabled: false,
  created_at: '2026-01-01T00:00:00Z',
  user_id: 1,
  owner: 'a@x',
  mine: true,
  ...over,
})

describe('harness 档锁协议（口径层 v1.48）', () => {
  it('Claude Code 锁 Anthropic、Codex 锁 Responses，其余不锁', () => {
    expect(lockedProtocol('claude-code')).toBe('anthropic')
    expect(lockedProtocol('codex')).toBe('responses')
    expect(lockedProtocol('curl')).toBeNull()
    expect(lockedProtocol('python')).toBeNull()
    expect(lockedProtocol('node')).toBeNull()
  })
})

describe('端点：Claude Code 给根、Codex 给 /v1，其余按协议（与 README 同源）', () => {
  it('harness 档端点', () => {
    expect(endpointOf('https://gw.example', 'anthropic', 'claude-code')).toBe('https://gw.example')
    expect(endpointOf('https://gw.example', 'responses', 'codex')).toBe('https://gw.example/v1')
  })
  it('协议端点', () => {
    expect(endpointOf('https://gw.example', 'chat', 'curl')).toBe(
      'https://gw.example/v1/chat/completions',
    )
    expect(endpointOf('https://gw.example', 'responses', 'curl')).toBe(
      'https://gw.example/v1/responses',
    )
    expect(endpointOf('https://gw.example', 'anthropic', 'curl')).toBe(
      'https://gw.example/v1/messages',
    )
  })
})

describe('Codex 的 model_context_window 取值序', () => {
  const m = (max: number, snap: number): GuideModel => ({
    id: 'x',
    direct: false,
    max_input_tokens: max,
    snapshot_context: snap,
  })
  it('条目已设优先，其次快照，再兑 200000 并注「按真实上限改」', () => {
    expect(contextWindow(m(300000, 1000000))).toEqual([300000, '选中模型的输入上限（估算）'])
    expect(contextWindow(m(0, 1000000))[0]).toBe(1000000)
    expect(contextWindow(null)).toEqual([200000, '取不到上限的兜底，按真实上限改'])
    expect(contextWindow(m(0, 0))).toEqual([200000, '取不到上限的兜底，按真实上限改'])
  })
})

describe('片段生成（README 同源）', () => {
  const facts = {
    base: 'https://gw.example',
    key: 'sk-ptg-abc',
    model: 'gw-sonnet',
    context: 200000,
    contextNote: '取不到上限的兜底，按真实上限改',
  }
  it('Claude Code 片段：根地址 + DEFAULT_HAIKU_MODEL 行', () => {
    const s = buildSnippet('anthropic', 'claude-code', facts)
    expect(s).toContain('export ANTHROPIC_BASE_URL=https://gw.example')
    expect(s).toContain('export ANTHROPIC_AUTH_TOKEN=sk-ptg-abc')
    expect(s).toContain('export ANTHROPIC_MODEL=gw-sonnet')
    expect(s).toContain('export ANTHROPIC_DEFAULT_HAIKU_MODEL=gw-sonnet')
    expect(s).toContain('claude')
    // 端点不带 /v1/messages——Claude Code 自己拼路径。
    expect(s).not.toContain('/v1/messages')
  })
  it('Codex 片段：base_url 到 /v1、wire_api responses、model_context_window 带注', () => {
    const s = buildSnippet('responses', 'codex', facts)
    expect(s).toContain('base_url = "https://gw.example/v1"')
    expect(s).toContain('wire_api = "responses"')
    expect(s).toContain('model = "gw-sonnet"')
    expect(s).toContain('model_context_window = 200000   # 取不到上限的兜底，按真实上限改')
  })
  it('curl 三协议打对应端点，Chat/Responses 走 Bearer、Anthropic 走 x-api-key', () => {
    const chat = buildSnippet('chat', 'curl', facts)
    expect(chat).toContain('curl https://gw.example/v1/chat/completions')
    expect(chat).toContain('Authorization: Bearer sk-ptg-abc')
    const resp = buildSnippet('responses', 'curl', facts)
    expect(resp).toContain('curl https://gw.example/v1/responses')
    const anth = buildSnippet('anthropic', 'curl', facts)
    expect(anth).toContain('curl https://gw.example/v1/messages')
    expect(anth).toContain('x-api-key: sk-ptg-abc')
    expect(anth).toContain('anthropic-version: 2023-06-01')
  })
  it('Python/Node 用官方 SDK，只改 base_url + api_key + model', () => {
    const py = buildSnippet('chat', 'python', facts)
    expect(py).toContain('from openai import OpenAI')
    expect(py).toContain('base_url="https://gw.example/v1"')
    expect(py).toContain('api_key="sk-ptg-abc"')
    expect(py).toContain('model="gw-sonnet"')
    const pyA = buildSnippet('anthropic', 'python', facts)
    expect(pyA).toContain('from anthropic import Anthropic')
    expect(pyA).toContain('base_url="https://gw.example"')
    const node = buildSnippet('responses', 'node', facts)
    expect(node).toContain('import OpenAI from "openai"')
    expect(node).toContain('client.responses.create')
    const nodeA = buildSnippet('anthropic', 'node', facts)
    expect(nodeA).toContain('import Anthropic from "@anthropic-ai/sdk"')
    expect(nodeA).toContain('baseURL: "https://gw.example"')
  })
  it('空态占位照出：无 key 兑 sk-ptg-…、无模型兑 your-model', () => {
    const s = buildSnippet('chat', 'curl', { ...facts, key: 'sk-ptg-…', model: 'your-model' })
    expect(s).toContain('sk-ptg-…')
    expect(s).toContain('your-model')
  })
})

describe('key 下拉过滤（#73/#182）', () => {
  it('只列本人的启用中 key：停用的、他人的都不进', () => {
    const out = selectableKeys([
      key({ id: 1, name: '我的' }),
      key({ id: 2, name: '停用的', disabled: true }),
      key({ id: 3, name: '别人的', mine: false }),
      key({ id: 4, name: '无主的', user_id: null, owner: '' }),
    ])
    expect(out.map((k) => k.name)).toEqual(['我的', '无主的'])
  })
})

describe('模型下拉按 key 白名单过滤', () => {
  const models: GuideModel[] = [
    { id: 'gw-a', direct: false, max_input_tokens: 0, snapshot_context: 0 },
    { id: 'ch/m', direct: true, max_input_tokens: 0, snapshot_context: 0 },
  ]
  it('* 与空都兑全部；逗号名单精确匹配', () => {
    expect(whitelisted(models, '*')).toHaveLength(2)
    expect(whitelisted(models, '')).toHaveLength(2)
    expect(whitelisted(models, 'gw-a').map((m) => m.id)).toEqual(['gw-a'])
    expect(whitelisted(models, 'ch/m,gw-a')).toHaveLength(2)
    expect(whitelisted(models, 'nope')).toHaveLength(0)
  })
})
