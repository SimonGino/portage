import { describe, expect, it } from 'vitest'
import type { Channel, Credential, ModelListResult } from '../../api'
import {
  cascadesToChannel,
  channelMark,
  filterChannels,
  fmtTokens,
  headerRows,
  headersOf,
  headersDirty,
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
  subscriptionSummary,
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
    headers: {},
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

describe('额外出站头', () => {
  it('行按头名排序，空集合出零行', () => {
    expect(headerRows({})).toEqual([])
    expect(headerRows({ b: '2', a: '1' })).toEqual([
      { name: 'a', value: '1' },
      { name: 'b', value: '2' },
    ])
  })

  it('去首尾空白、整行空的丢掉；半空行照发交服务端闸报', () => {
    const r = headersOf([
      { name: ' x-a ', value: ' 1 ' },
      { name: '', value: '' },
      { name: 'x-b', value: '' },
    ])
    expect(r).toEqual({ headers: { 'x-a': '1', 'x-b': '' }, dup: '' })
  })

  it('同名两行报出来，不静默吞掉一行', () => {
    expect(headersOf([
      { name: 'x-a', value: '1' },
      { name: 'x-a', value: '2' },
    ]).dup).toBe('x-a')
  })

  it('Object 原型上的名字只是普通头名', () => {
    const r = headersOf([
      { name: 'constructor', value: '1' },
      { name: '__proto__', value: '2' },
    ])
    expect(r.dup).toBe('')
    expect(JSON.stringify(r.headers)).toBe('{"constructor":"1","__proto__":"2"}')
  })

  it('改动判据不看行序与整行空行', () => {
    const saved = { 'x-a': '1', 'x-b': '2' }
    expect(headersDirty(saved, [
      { name: 'x-b', value: '2' },
      { name: 'x-a', value: '1' },
      { name: '', value: '' },
    ])).toBe(false)
    expect(headersDirty(saved, [{ name: 'x-a', value: '1' }])).toBe(true)
    expect(headersDirty(saved, [...headerRows(saved), { name: 'x-c', value: '3' }])).toBe(true)
    expect(headersDirty({}, [{ name: '', value: '' }])).toBe(false)
  })
})

// ── 订阅渠道的凭证行（#212，DESIGN v0.77）──────────────────────────────

/** 签一枚假 JWT：只在意 payload 段（展示层不验签）。 */
function jwt(claims: Record<string, unknown>): string {
  const b64 = (o: unknown) =>
    btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64(claims)}.sig`
}

function siwcValue(over: Record<string, unknown> = {}): string {
  return JSON.stringify({
    client_id: 'oaiapp-1',
    host_id: 'urn:uuid:h',
    sub: 'user-123',
    email: 'po@example.com',
    id_token: jwt({
      iss: 'https://auth.openai.com',
      aud: 'oaiapp-1',
      'https://api.openai.com/auth': { chatgpt_plan_type: 'pro' },
    }),
    access_token: 'at',
    refresh_token: 'rt',
    expires_at: 1791470000,
    scopes: ['chatgpt.tokens.use.direct'],
    ...over,
  })
}

describe('订阅凭证行读数', () => {
  it('解出账号（email）· 套餐（ID token claims）· 过期时刻', () => {
    const s = subscriptionSummary(siwcValue())
    expect(s).not.toBeNull()
    expect(s!.account).toBe('po@example.com')
    expect(s!.plan).toBe('pro')
    expect(s!.expiresAt).toBe(1791470000)
  })

  it('ID token 没有套餐时退化到 access token 的 claims（magpie siwcPlan 同判）', () => {
    const s = subscriptionSummary(
      siwcValue({
        id_token: 'not-a-jwt',
        access_token: jwt({ 'https://api.openai.com/auth': { chatgpt_plan_type: 'plus' } }),
      }),
    )
    expect(s!.plan).toBe('plus')
  })

  it('email 缺了用 sub 兜底，两边都没有才空串', () => {
    expect(subscriptionSummary(siwcValue({ email: '' }))!.account).toBe('user-123')
    expect(subscriptionSummary(siwcValue({ email: '', sub: '' }))!.account).toBe('')
  })

  it('值不是 JSON / 不是凭证形状时回 null，不抛', () => {
    expect(subscriptionSummary('sk-不是JSON')).toBeNull()
    expect(subscriptionSummary('123')).toBeNull()
    expect(subscriptionSummary('{}')).toBeNull()
    expect(subscriptionSummary('')).toBeNull()
  })
})
