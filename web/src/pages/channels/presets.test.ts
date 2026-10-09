import { describe, expect, it } from 'vitest'
import type { ChannelPreset } from '../../api'
import { channelCreatePayload, filterPresets, preChecked, presetPlans } from './derive'
import type { ChannelCreateDraft } from './derive'

const ds: ChannelPreset = {
  id: 'deepseek',
  name: 'DeepSeek',
  group: 'vendor',
  icon: 'deepseek',
  models_dev: 'deepseek',
  protocols: { openai: 'https://api.deepseek.com', anthropic: 'https://api.deepseek.com/anthropic' },
  keys_url: 'https://platform.deepseek.com/api_keys',
}
const kimi: ChannelPreset = {
  id: 'kimi',
  name: 'Kimi',
  group: 'vendor',
  icon: 'moonshot',
  keys_url: 'https://platform.moonshot.ai/console/api-keys',
  plans: [
    { id: 'global', name: '国际', models_dev: 'moonshotai', protocols: { openai: 'https://api.moonshot.ai' } },
    { id: 'cn', name: '中国', models_dev: 'moonshotai-cn', protocols: { openai: 'https://api.moonshot.cn' } },
  ],
}
// 订阅组条目（#216，口径层 §2.2 v1.52）：无 keys_url、带 credential_type、不填
// models_dev——目录搜索照常按名 / id / 域名命中，tile 渲染不吃这个形状的特殊性。
const chatgpt: ChannelPreset = {
  id: 'chatgpt',
  name: 'ChatGPT',
  group: 'subscription',
  icon: 'openai',
  credential_type: 'chatgpt_account',
  protocols: { openai_responses: 'https://api.openai.com' },
}

describe('filterPresets', () => {
  it('空查询原样回', () => {
    expect(filterPresets([ds, kimi], '  ')).toEqual([ds, kimi])
  })
  it('按名、id 不分大小写', () => {
    expect(filterPresets([ds, kimi], 'KIM')).toEqual([kimi])
    expect(filterPresets([ds, kimi], 'deepseek')).toEqual([ds])
  })
  it('按域名，含 plans 里的地址', () => {
    expect(filterPresets([ds, kimi], 'moonshot.cn')).toEqual([kimi])
    expect(filterPresets([ds, kimi], 'api.deepseek')).toEqual([ds])
  })
  it('不拿路径凑命中', () => {
    expect(filterPresets([ds, kimi], 'anthropic')).toEqual([])
  })
})

describe('presetPlans', () => {
  it('无 plans 时顶层地址当唯一一套', () => {
    expect(presetPlans(ds)).toEqual([{ id: '', name: '', models_dev: 'deepseek', protocols: ds.protocols }])
  })
  it('有 plans 原样回', () => {
    expect(presetPlans(kimi)).toBe(kimi.plans)
  })
  it('订阅条目无 plans 时顶层地址当唯一一套，models_dev 不填 → 建成未标注', () => {
    const [only] = presetPlans(chatgpt)
    expect(only.protocols).toEqual(chatgpt.protocols)
    expect(only.models_dev ?? '').toBe('')
  })
})

describe('订阅组预设（#216）', () => {
  it('搜索按名 / id 命中订阅 tile，无 keys_url 不炸', () => {
    expect(filterPresets([ds, chatgpt], 'chatgpt')).toEqual([chatgpt])
    expect(filterPresets([ds, chatgpt], 'ChatGPT')).toEqual([chatgpt])
  })
  it('按域名命中订阅 tile', () => {
    expect(filterPresets([ds, chatgpt], 'openai.com')).toEqual([chatgpt])
  })
})

describe('preChecked', () => {
  it('上游 ∩ 建议，已纳管的不算', () => {
    expect([...preChecked(['a', 'b', 'c', 'x'], ['b', 'c', 'd'], new Set(['c']))]).toEqual(['b'])
  })
  it('没有建议就什么都不勾', () => {
    expect(preChecked(['a'], [], new Set()).size).toBe(0)
  })
})

// 建渠道请求体（#216）：表单里「登录」与「创建」共用的那笔（差在带不带 key）。
// 钉住的是条件随行的三位——尤其订阅预设的 credential_type：丢了渠道会静默建成
// api_key，点「登录」只能吃 400，页面上什么都看不出来。
describe('channelCreatePayload（#216）', () => {
  const draft: ChannelCreateDraft = {
    name: 'chatgpt',
    urls: { openai_responses: 'https://api.openai.com' },
    maxConcurrency: 0,
    provider: '',
    authScheme: 'default',
    compaction: false,
    stateful: true,
  }
  it('订阅预设随 credential_type——即表单的 preset?.credential_type 透传', () => {
    const p = channelCreatePayload({ ...draft, credentialType: chatgpt.credential_type })
    expect(p.credential_type).toBe('chatgpt_account')
  })
  it('厂商预设与自定义不带这键，渠道落默认 api_key', () => {
    const p = channelCreatePayload({ ...draft, credentialType: ds.credential_type })
    expect('credential_type' in p).toBe(false)
  })
  it('地址剔空串；能力位只在声明 Responses 时随行', () => {
    const neither = channelCreatePayload({ ...draft, urls: { openai: 'https://x.com', openai_responses: '  ' } })
    expect(neither.base_url).toEqual({ openai: 'https://x.com' })
    expect('supports_compaction' in neither).toBe(false)
    expect('supports_stateful_responses' in neither).toBe(false)
    const both = channelCreatePayload(draft)
    expect(both.supports_compaction).toBe(false)
    expect(both.supports_stateful_responses).toBe(true)
  })
})
