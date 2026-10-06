import { describe, it, expect, vi } from 'vitest'
import en from './en.json'

vi.mock('../dataProvider', () => ({ default: { getOne: vi.fn() } }))

const countLeaves = (obj) =>
  Object.values(obj).reduce(
    (sum, v) =>
      sum + (typeof v === 'object' && v !== null ? countLeaves(v) : v ? 1 : 0),
    0,
  )

describe('i18n provider', () => {
  // 这里动态导入整个 i18n provider（含全部翻译表），全量并发跑测试时可能超过默认 5s 超时
  // （单跑约 2.7s），因此显式给足超时，避免把「机器忙」误报成失败。
  it(
    'does not mutate the bundled English translations',
    async () => {
      const before = countLeaves(en)
      await import('./provider')
      expect(countLeaves(en)).toEqual(before)
    },
    15000,
  )
})
