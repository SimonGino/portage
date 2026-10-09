import { describe, expect, it } from 'vitest'
import type { ChannelPreset } from '../../api'
import { filterPresets, preChecked, presetPlans } from './derive'

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
})

describe('preChecked', () => {
  it('上游 ∩ 建议，已纳管的不算', () => {
    expect([...preChecked(['a', 'b', 'c', 'x'], ['b', 'c', 'd'], new Set(['c']))]).toEqual(['b'])
  })
  it('没有建议就什么都不勾', () => {
    expect(preChecked(['a'], [], new Set()).size).toBe(0)
  })
})
