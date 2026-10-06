import React from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, beforeEach, afterEach, vi, expect } from 'vitest'

// 提到最外层供 vi.mock 工厂使用（vi.mock 会被提升到文件顶部执行）
const { notifyMock } = vi.hoisted(() => ({ notifyMock: vi.fn() }))

vi.mock('react-admin', () => ({
  useNotify: () => notifyMock,
  useTranslate: () => (key) => key,
}))

// 只关心「试听」相关接口的行为，其余接口给出安全默认值
vi.mock('./api', () => ({
  searchHealth: vi.fn(),
  searchMusic: vi.fn(),
  getLyric: vi.fn(),
  startDownload: vi.fn(),
  downloadStatus: vi.fn(),
  cancelDownload: vi.fn(),
  loginSources: vi.fn().mockResolvedValue({}),
  loginCreate: vi.fn(),
  loginPoll: vi.fn(),
  loginState: vi.fn().mockResolvedValue({}),
  previewSetting: vi.fn().mockResolvedValue({ preview: true }),
  previewUrl: (it) => `preview://${it.source}/${it.id}`,
  probePreview: vi.fn(),
  previewErrorMessage: ({ status, error } = {}) =>
    `ERR(${status || '-'}:${error || '-'})`,
  errMessage: (e) => String(e),
}))

import Search from './Search'
import * as api from './api'

// 同名歌曲在 QQ 与网易云各有一条（真实搜索结果就是按平台各返回一份）
const SONG_QQ = {
  id: '1',
  source: 'qq',
  name: '稻香',
  artist: '周杰伦',
  album: '魔杰座',
  duration: 223,
  size: 9267244,
  bitrate: 320,
}
const SONG_NETEASE = { ...SONG_QQ, id: '2', source: 'netease' }

describe('<Search /> 试听', () => {
  beforeEach(() => {
    notifyMock.mockClear()
    api.searchHealth.mockResolvedValue({
      enabled: true,
      reachable: true,
      preview: true,
      dir: '/opt/navidrome/music',
    })
    api.searchMusic.mockResolvedValue({
      data: { songs: [SONG_QQ, SONG_NETEASE] },
    })
    api.downloadStatus.mockResolvedValue({ running: false, total: 0, processed: 0 })
    // jsdom 未实现媒体播放：打桩，避免 play() 返回 undefined 导致 .then 报错
    window.HTMLMediaElement.prototype.play = vi.fn().mockResolvedValue()
    window.HTMLMediaElement.prototype.pause = vi.fn()
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  // 触发一次搜索并等待结果表格出现（试听按钮按 aria-label 定位）
  const renderAndSearch = async () => {
    const utils = render(<Search />)
    const box = screen.getByRole('textbox')
    fireEvent.change(box, { target: { value: '稻香' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(screen.getAllByLabelText('试听')).toHaveLength(2))
    return utils
  }

  it('首条平台无音源时自动切换到同名其它平台并播放', async () => {
    api.probePreview.mockImplementation((url) =>
      url.includes('/qq/')
        ? Promise.resolve({ ok: false, status: 502, error: '音源不可用' })
        : Promise.resolve({ ok: true, status: 206, error: '' }),
    )

    const { container } = await renderAndSearch()
    fireEvent.click(screen.getAllByLabelText('试听')[0])

    await waitFor(() => {
      expect(container.querySelector('audio').getAttribute('src')).toContain(
        'netease/2',
      )
    })
    expect(notifyMock).toHaveBeenCalledWith(
      expect.stringContaining('已自动切换'),
      'info',
    )
  })

  it('所有平台都无音源时提示后端返回的真实原因', async () => {
    api.probePreview.mockResolvedValue({
      ok: false,
      status: 502,
      error: '音源不可用（上游返回 404）',
    })

    await renderAndSearch()
    fireEvent.click(screen.getAllByLabelText('试听')[0])

    await waitFor(() => {
      expect(notifyMock).toHaveBeenCalledWith(
        expect.stringContaining('试听失败：ERR(502:音源不可用（上游返回 404）)'),
        'error',
      )
    })
  })

  // 酷狗平台侧不下发直链，默认勾选只会让用户频繁撞到「无可用音源」
  it('默认搜索平台不含酷狗，仍含网易云/酷我/咪咕', async () => {
    await renderAndSearch()
    expect(api.searchMusic).toHaveBeenCalledWith(
      expect.objectContaining({ sources: 'netease,kuwo,migu' }),
    )
  })
})
