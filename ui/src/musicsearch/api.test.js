import { describe, it, beforeEach, afterEach, vi, expect } from 'vitest'
import { previewUrl, probePreview, previewErrorMessage } from './api'
import config from '../config'

// 构造可控制的 fetch 响应替身：只用到 ok/status/json/body.cancel
const mockResponse = ({ ok, status, json, cancel }) => ({
  ok,
  status,
  json: json
    ? () => Promise.resolve(json)
    : () => Promise.reject(new Error('响应体不是 JSON')),
  body: { cancel: cancel || vi.fn() },
})

describe('previewUrl', () => {
  const originalBase = config.baseURL

  beforeEach(() => {
    localStorage.setItem('token', 'tok-123')
  })

  afterEach(() => {
    config.baseURL = originalBase
  })

  // 生产通过 nginx 子路径 /music 反代，漏掉前缀会打到站根被路由到博客后端
  it('baseURL 非空时带上子路径前缀', () => {
    config.baseURL = '/music'
    expect(previewUrl({ id: 'a b', source: 'qq' })).toBe(
      '/music/api/musicsearch/preview?id=a%20b&source=qq&jwt=tok-123',
    )
  })

  it('baseURL 为空时保持站内绝对路径', () => {
    config.baseURL = ''
    expect(previewUrl({ id: '1', source: 'netease' })).toBe(
      '/api/musicsearch/preview?id=1&source=netease&jwt=tok-123',
    )
  })

  it('source 缺省时不出现 undefined', () => {
    config.baseURL = ''
    expect(previewUrl({ id: '1' })).toBe(
      '/api/musicsearch/preview?id=1&source=&jwt=tok-123',
    )
  })
})

describe('probePreview', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('可播放时只取 1 字节并立即取消响应体', async () => {
    const cancel = vi.fn().mockResolvedValue(undefined)
    const fetchMock = vi
      .fn()
      .mockResolvedValue(mockResponse({ ok: true, status: 206, cancel }))
    global.fetch = fetchMock

    await expect(probePreview('/api/preview')).resolves.toEqual({
      ok: true,
      status: 206,
      error: '',
    })

    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/preview')
    expect(options.headers.Range).toBe('bytes=0-0')
    // 预检只关心状态码，不能把整首歌下载一遍
    expect(cancel).toHaveBeenCalledTimes(1)
  })

  it('不可播放时透出后端 error 文本', async () => {
    global.fetch = vi.fn().mockResolvedValue(
      mockResponse({
        ok: false,
        status: 502,
        json: { error: '音源不可用（上游返回 404，可能是版权限制或无可用音源）' },
      }),
    )

    const probe = await probePreview('/api/preview')
    expect(probe.ok).toBe(false)
    expect(probe.status).toBe(502)
    expect(probe.error).toContain('音源不可用')
  })

  it('响应体不是 JSON 时不抛异常', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: () => Promise.reject(new Error('bad json')),
      body: { cancel: vi.fn() },
    })

    await expect(probePreview('/api/preview')).resolves.toEqual({
      ok: false,
      status: 500,
      error: '',
    })
  })
})

describe('previewErrorMessage', () => {
  it('401 归一为登录过期提示', () => {
    expect(
      previewErrorMessage({ status: 401, error: 'Not authenticated' }),
    ).toContain('登录已过期')
  })

  it('优先透出后端错误文本（如试听开关未开）', () => {
    expect(
      previewErrorMessage({
        status: 403,
        error: '在线试听未启用（在搜索页顶部打开开关）',
      }),
    ).toBe('在线试听未启用（在搜索页顶部打开开关）')
  })

  it('没有错误文本时给出兜底提示', () => {
    expect(previewErrorMessage({ status: 502, error: '' })).toContain(
      'HTTP 502',
    )
  })

  it('502 有错误文本时追加换平台建议', () => {
    expect(
      previewErrorMessage({
        status: 502,
        error: '音源不可用（上游返回 404，可能是版权限制或无可用音源）',
      }),
    ).toBe(
      '音源不可用（上游返回 404，可能是版权限制或无可用音源）。可切换其它平台重试',
    )
  })

  it('预检结果为空时不抛异常', () => {
    expect(previewErrorMessage()).toBe('音源不可用，可切换其它平台重试')
  })
})
