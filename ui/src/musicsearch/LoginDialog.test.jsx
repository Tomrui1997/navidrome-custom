import React from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, beforeEach, afterEach, vi, expect } from 'vitest'

const { notifyMock } = vi.hoisted(() => ({ notifyMock: vi.fn() }))

vi.mock('react-admin', () => ({
  useNotify: () => notifyMock,
  useTranslate: () => (key) => key,
}))

vi.mock('./api', () => ({
  loginState: vi.fn().mockResolvedValue({ data: {} }),
  loginCreate: vi.fn(),
  loginPoll: vi.fn().mockResolvedValue({
    data: { status: 'waiting', message: '' },
  }),
  errMessage: (e) => String(e),
}))

// 二维码用替身表示，便于断言「是否渲染了二维码」
vi.mock('qrcode.react', () => ({
  QRCodeSVG: ({ value }) => <div data-testid="qr-code">{value}</div>,
}))

import LoginDialog from './LoginDialog'
import * as api from './api'

// 打开平台下拉并选中指定平台
const pickSource = async (label) => {
  const selectNode = screen
    .getAllByRole('button')
    .find((el) => /QQ音乐|酷狗音乐|网易云|Bilibili/.test(el.textContent))
  fireEvent.mouseDown(selectNode)
  fireEvent.click(await screen.findByRole('option', { name: label }))
}

describe('<LoginDialog />', () => {
  beforeEach(() => {
    notifyMock.mockClear()
    api.loginCreate.mockResolvedValue({
      data: { url: 'https://example.com/sign-in?token=abc', key: 'k-1' },
    })
    window.open = vi.fn()
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('酷狗改用链接登录：不渲染二维码，给复制/打开链接按钮', async () => {
    render(<LoginDialog open onClose={() => {}} onChanged={() => {}} />)
    await pickSource('酷狗音乐（链接登录）')

    // 按钮文案随平台切换为「获取登录链接」
    fireEvent.click(
      screen.getByRole('button', {
        name: 'resources.musicsearch.login.getLink',
      }),
    )

    await waitFor(() => {
      expect(
        screen.getByText('https://example.com/sign-in?token=abc'),
      ).toBeInTheDocument()
    })
    // 链接登录形态：没有二维码，有「打开链接」
    expect(screen.queryByTestId('qr-code')).toBeNull()
    fireEvent.click(
      screen.getByRole('button', { name: 'resources.musicsearch.login.open' }),
    )
    expect(window.open).toHaveBeenCalledWith(
      'https://example.com/sign-in?token=abc',
      '_blank',
      'noopener',
    )
  })

  it('其它链接型平台仍渲染二维码（不回归）', async () => {
    render(<LoginDialog open onClose={() => {}} onChanged={() => {}} />)
    await pickSource('网易云音乐')

    fireEvent.click(
      screen.getByRole('button', {
        name: 'resources.musicsearch.login.getQr',
      }),
    )

    await waitFor(() => {
      expect(screen.getByTestId('qr-code')).toBeInTheDocument()
    })
    expect(
      screen.queryByRole('button', { name: 'resources.musicsearch.login.open' }),
    ).toBeNull()
  })
})
