import { describe, expect, it } from 'vitest'
import { providerName, providerOptions } from './prices'

const list = [
  { id: '302ai', name: '302 AI' },
  { id: 'openai', name: 'OpenAI' },
]

describe('providerOptions', () => {
  it('「未标注」打头，名单按序跟上，hint 是 id', () => {
    expect(providerOptions(list, '')).toEqual([
      { value: '', label: '未标注' },
      { value: '302ai', label: '302 AI', hint: '302ai' },
      { value: 'openai', label: 'OpenAI', hint: 'openai' },
    ])
  })

  it('当前值在名单里就不补项', () => {
    expect(providerOptions(list, 'openai')).toHaveLength(3)
  })

  it('当前值不在名单里：补一项回去并注明「快照名单外」，触发器不会显示成「未标注」', () => {
    expect(providerOptions(list, 'mystery')[1]).toEqual({ value: 'mystery', label: 'mystery', hint: '快照名单外' })
    // 名单还没拉到（空数组）同样补，库里明明有值不能显示成未标注。
    expect(providerOptions([], 'openai')).toEqual([
      { value: '', label: '未标注' },
      { value: 'openai', label: 'openai', hint: '快照名单外' },
    ])
  })
})

describe('providerName', () => {
  it('名单里有摆人话名，没有（或没拉到）摆 id 本身', () => {
    expect(providerName(list, '302ai')).toBe('302 AI')
    expect(providerName(list, 'mystery')).toBe('mystery')
    expect(providerName([], 'openai')).toBe('openai')
  })
})
