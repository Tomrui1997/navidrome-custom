import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import config from './config'
import { bootstrapLocale, applyHtmlLang } from './bootstrapLocale'

// 模拟服务端 /api/translation/:id 返回的记录结构
const TRANSLATION = {
  id: 'zh-Hans',
  name: '简体中文',
  data: '{"resources":{}}',
  termCount: 1,
}

const mockFetchOk = () =>
  vi.fn().mockResolvedValue({ ok: true, json: async () => TRANSLATION })

describe('bootstrapLocale', () => {
  const originalLang = config.defaultLanguage

  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    config.defaultLanguage = originalLang
    vi.restoreAllMocks()
  })

  it('首次访问时先取默认语言翻译，保证首屏即为中文', async () => {
    config.defaultLanguage = 'zh-Hans'
    global.fetch = mockFetchOk()

    await expect(bootstrapLocale()).resolves.toBe('zh-Hans')
    expect(global.fetch).toHaveBeenCalledTimes(1)
    expect(JSON.parse(localStorage.getItem('translation')).id).toBe('zh-Hans')
    expect(localStorage.getItem('locale')).toBe('zh-Hans')
  })

  it('已有匹配缓存时不发起请求', async () => {
    config.defaultLanguage = 'zh-Hans'
    localStorage.setItem('translation', JSON.stringify(TRANSLATION))
    localStorage.setItem('locale', 'zh-Hans')
    global.fetch = mockFetchOk()

    await expect(bootstrapLocale()).resolves.toBe('zh-Hans')
    expect(global.fetch).not.toHaveBeenCalled()
  })

  it('有 locale 但翻译缓存丢失时补取，避免卡在英文', async () => {
    config.defaultLanguage = 'zh-Hans'
    localStorage.setItem('locale', 'zh-Hans')
    global.fetch = mockFetchOk()

    await expect(bootstrapLocale()).resolves.toBe('zh-Hans')
    expect(global.fetch).toHaveBeenCalledTimes(1)
  })

  it('用户手动选过其他语言时尊重该选择', async () => {
    config.defaultLanguage = 'zh-Hans'
    localStorage.setItem('locale', 'ja')
    global.fetch = vi
      .fn()
      .mockResolvedValue({
        ok: true,
        json: async () => ({ ...TRANSLATION, id: 'ja' }),
      })

    await expect(bootstrapLocale()).resolves.toBe('ja')
    expect(JSON.parse(localStorage.getItem('translation')).id).toBe('ja')
  })

  it('英文为内置语言，不需要请求', async () => {
    config.defaultLanguage = 'en'
    global.fetch = mockFetchOk()

    await expect(bootstrapLocale()).resolves.toBe('en')
    expect(global.fetch).not.toHaveBeenCalled()
  })

  it('请求失败时回退英文且不抛错', async () => {
    config.defaultLanguage = 'zh-Hans'
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 500 })
    vi.spyOn(console, 'error').mockImplementation(() => {})

    await expect(bootstrapLocale()).resolves.toBe('en')
    expect(localStorage.getItem('locale')).toBeNull()
  })

  it('未配置默认语言时保持原有英文兜底', async () => {
    config.defaultLanguage = ''
    global.fetch = mockFetchOk()

    await expect(bootstrapLocale()).resolves.toBe('en')
    expect(global.fetch).not.toHaveBeenCalled()
  })
})

describe('applyHtmlLang', () => {
  it('把语言写入 <html lang>', () => {
    applyHtmlLang('zh-Hans')
    expect(document.documentElement.lang).toBe('zh-Hans')
    applyHtmlLang('')
    expect(document.documentElement.lang).toBe('en')
  })
})
