import { describe, expect, it } from 'vitest'
import type { Channel, Credential, ModelListResult } from '../../api'
import {
  cascadesToChannel,
  channelMark,
  filterChannels,
  fmtTokens,
  limitToSave,
  listComplete,
  listedOn,
  maxConcurrencyOf,
  normalizeProtocols,
  parseTokens,
  primaryCredential,
  protocolsToAdd,
  sameBaseURLs,
  settingsDirty,
  sortChannels,
  splitModelNames,
  staleProtocols,
  suggestProtocols,
} from './derive'

function ch(over: Partial<Channel>): Channel {
  return {
    id: 1,
    name: 'c',
    protocols: ['openai', 'anthropic'],
    base_url: { openai: 'https://a.example', anthropic: 'https://b.example' },
    key_mode: 'polling',
    auth_scheme: 'default',
    max_concurrency: 0,
    supports_compaction: false,
    supports_stateful_responses: true,
    provider: '',
    disabled: false,
    enabled_keys: 1,
    disabled_keys: 0,
    models: [],
    ...over,
  } as Channel
}

function cred(over: Partial<Credential>): Credential {
  return {
    id: 1,
    name: 'k',
    credential: 'sk',
    disabled: false,
    disabled_reason: '',
    disabled_at: '',
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

describe('左栏清单', () => {
  it('停用的沉底，组内按 id 不随改名跳位', () => {
    const got = sortChannels([
      ch({ id: 3, name: 'a', disabled: true }),
      ch({ id: 2, name: 'z' }),
      ch({ id: 1, name: 'm', disabled: true }),
      ch({ id: 5, name: 'b' }),
    ]).map((c) => c.id)
    expect(got).toEqual([2, 5, 1, 3])
  })

  it('搜索命中任意一个协议的地址，不只第一份', () => {
    const list = [
      ch({ id: 1, name: 'one', base_url: { openai: 'https://x', anthropic: 'https://claude.proxy' } }),
      ch({ id: 2, name: 'two', base_url: { openai: 'https://y' } }),
    ]
    expect(filterChannels(list, 'CLAUDE').map((c) => c.id)).toEqual([1])
    expect(filterChannels(list, 'two').map((c) => c.id)).toEqual([2])
    expect(filterChannels(list, '  ').map((c) => c.id)).toEqual([1, 2])
  })

  it('标记三档互斥，停用压过缺凭证压过无协议', () => {
    expect(channelMark(ch({ disabled: true, enabled_keys: 0, protocols: [] }))).toBe('停用')
    expect(channelMark(ch({ enabled_keys: 0, protocols: [] }))).toBe('缺凭证')
    expect(channelMark(ch({ protocols: [] }))).toBe('无协议')
    expect(channelMark(ch({}))).toBe('')
  })
})

describe('上游列表给的证据', () => {
  const protos = ['openai', 'anthropic'] as const
  const both: ModelListResult[] = [
    { protocols: ['openai', 'openai_responses'], models: ['gpt', 'shared'], status: 200, detail: '' },
    { protocols: ['anthropic'], models: ['claude', 'shared'], status: 200, detail: '' },
  ]

  it('listedOn 只保留渠道自己声明的协议，没拉过回空', () => {
    expect(listedOn(both, [...protos], 'shared')).toEqual(['openai', 'anthropic'])
    expect(listedOn(both, [...protos], 'gpt')).toEqual(['openai'])
    expect(listedOn(null, [...protos], 'gpt')).toEqual([])
  })

  it('证据不全不推断：一侧 models 为 null 就不算齐', () => {
    expect(listComplete(both, [...protos])).toBe(true)
    const half: ModelListResult[] = [both[0], { ...both[1], models: null, status: 401 }]
    expect(listComplete(half, [...protos])).toBe(false)
    expect(listComplete(null, [...protos])).toBe(false)
    // 渠道只声明一侧，那一侧拉到了就算齐
    expect(listComplete(half, ['openai'])).toBe(true)
  })

  it('加模型时只在证据齐全且只列出一部分时写子集，否则写空 = 继承', () => {
    expect(protocolsToAdd(['openai'], true, [...protos])).toEqual(['openai'])
    expect(protocolsToAdd(['openai'], false, [...protos])).toEqual([])
    expect(protocolsToAdd(['openai', 'anthropic'], true, [...protos])).toEqual([])
    // 证据齐全但哪一侧都没列：写空，不凭零证据砍路径
    expect(protocolsToAdd([], true, [...protos])).toEqual([])
  })

  it('建议只在「确实只列出一部分」时给', () => {
    expect(suggestProtocols(['openai'], true, [...protos])).toEqual(['openai'])
    expect(suggestProtocols([], true, [...protos])).toBeNull()
    expect(suggestProtocols(['openai'], false, [...protos])).toBeNull()
    expect(suggestProtocols(['openai', 'anthropic'], true, [...protos])).toBeNull()
  })
})

describe('模型的协议子集', () => {
  const protos = ['openai', 'anthropic'] as const

  it('勾满归 []（等价继承），部分勾选按渠道序落', () => {
    expect(normalizeProtocols(['anthropic', 'openai'], [...protos])).toEqual([])
    expect(normalizeProtocols(['anthropic'], [...protos])).toEqual(['anthropic'])
  })

  it('还留着失效项时不归零：失效项跟在渠道内的后面', () => {
    expect(normalizeProtocols(['openai', 'anthropic', 'openai_responses'], [...protos])).toEqual([
      'openai',
      'anthropic',
      'openai_responses',
    ])
    expect(staleProtocols(['openai_responses', 'openai'], [...protos])).toEqual(['openai_responses'])
  })
})

describe('输入上限', () => {
  it('紧凑形来回', () => {
    expect(fmtTokens(200000)).toBe('200k')
    expect(fmtTokens(1500)).toBe('1,500')
    expect(parseTokens('200k')).toBe(200000)
    expect(parseTokens(' 1M ')).toBe(1000000)
    expect(parseTokens('12')).toBe(12)
    expect(parseTokens('1.5k')).toBeNull()
    expect(parseTokens('')).toBeNull()
  })

  it('清空 = 清成不限（0），不是没改；同值与解析不出都不写', () => {
    expect(limitToSave('', 200000)).toBe(0)
    expect(limitToSave('', 0)).toBeNull()
    expect(limitToSave('200k', 200000)).toBeNull()
    expect(limitToSave('abc', 200000)).toBeNull()
    expect(limitToSave('8k', 0)).toBe(8000)
  })
})

describe('手动添加模型', () => {
  it('逗号、空格、换行、中文顿号都算分隔，去重，已纳管的跳过并计数', () => {
    const { fresh, dupes } = splitModelNames('a, b\nc、a，d  b', new Set(['b']))
    expect(fresh).toEqual(['a', 'c', 'd'])
    expect(dupes).toBe(1)
  })
})

describe('API 地址', () => {
  it('逐协议 trim 比对，尾随空格不算改动', () => {
    expect(sameBaseURLs({ openai: 'https://x ' }, { openai: 'https://x' })).toBe(true)
    expect(sameBaseURLs({ openai: 'https://x' }, { openai: 'https://x', anthropic: 'https://y' })).toBe(false)
    expect(sameBaseURLs({}, { openai: '' })).toBe(true)
  })
})

describe('凭证池', () => {
  it('在用的那把是第一把启用的', () => {
    const list = [cred({ id: 1, disabled: true }), cred({ id: 2 }), cred({ id: 3 })]
    expect(primaryCredential(list)?.id).toBe(2)
    expect(primaryCredential([cred({ disabled: true })])).toBeNull()
  })

  it('只有启用渠道的最后一把启用凭证才连带停渠道', () => {
    expect(cascadesToChannel(cred({}), 1, ch({}))).toBe(true)
    expect(cascadesToChannel(cred({}), 2, ch({}))).toBe(false)
    expect(cascadesToChannel(cred({ disabled: true }), 1, ch({}))).toBe(false)
    expect(cascadesToChannel(cred({}), 1, ch({ disabled: true }))).toBe(false)
  })
})

describe('上游设置表单', () => {
  it('并发上限空与非数字归 0', () => {
    expect(maxConcurrencyOf('')).toBe(0)
    expect(maxConcurrencyOf('x')).toBe(0)
    expect(maxConcurrencyOf('-1')).toBe(0)
    expect(maxConcurrencyOf('4')).toBe(4)
  })

  it('能力位只在露着时算改动', () => {
    const c = ch({ supports_compaction: false })
    const same = {
      name: c.name,
      maxConcurrency: 0,
      provider: '',
      authScheme: 'default' as const,
      compaction: true,
      stateful: true,
    }
    expect(settingsDirty(c, same, false)).toBe(false)
    expect(settingsDirty(c, same, true)).toBe(true)
    expect(settingsDirty(c, { ...same, compaction: false, name: 'renamed' }, false)).toBe(true)
  })
})
