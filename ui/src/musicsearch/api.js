import httpClient from '../dataProvider/httpClient'
import { REST_URL } from '../consts'
import { baseUrl } from '../utils'

// 与后端新增的「在线搜索」接口对接（均为管理员专用）。
// 说明：httpClient 在非 2xx 时会抛出 HttpError（body 里带 error 字段），调用方需捕获。

export const searchHealth = () =>
  httpClient(`${REST_URL}/musicsearch/health`).then((r) => r.json)

export const searchMusic = ({ q, type = 'song', sources = '' }) => {
  const params = new URLSearchParams({ q, type })
  if (sources) params.set('sources', sources)
  return httpClient(`${REST_URL}/musicsearch/search?${params.toString()}`).then(
    (r) => r.json,
  )
}

export const getLyric = ({ id, source }) =>
  httpClient(
    `${REST_URL}/musicsearch/lyric?id=${encodeURIComponent(
      id,
    )}&source=${encodeURIComponent(source)}`,
  ).then((r) => r.json)

export const startDownload = ({ items, types }) =>
  httpClient(`${REST_URL}/musicsearch/download`, {
    method: 'POST',
    body: JSON.stringify({ items, types }),
  }).then((r) => r.json)

export const downloadStatus = () =>
  httpClient(`${REST_URL}/musicsearch/download/status`).then((r) => r.json)

export const cancelDownload = () =>
  httpClient(`${REST_URL}/musicsearch/download/cancel`, {
    method: 'POST',
  }).then((r) => r.json)

// errMessage 把 HttpError 的 body.error 提取为可读信息。
export const errMessage = (err) => {
  const body = err && err.body
  if (body) {
    if (typeof body === 'string') return body
    if (body.error) return body.error
  }
  return (err && err.message) || '未知错误'
}

// ------ 账号登录（扫码）：登录后可解锁更高音质 ------

export const loginSources = () =>
  httpClient(`${REST_URL}/musicsearch/login/sources`).then((r) => r.json)

export const loginCreate = (source) =>
  httpClient(`${REST_URL}/musicsearch/login/qr`, {
    method: 'POST',
    body: JSON.stringify({ source }),
  }).then((r) => r.json)

export const loginPoll = ({ source, key }) =>
  httpClient(
    `${REST_URL}/musicsearch/login/qr/status?source=${encodeURIComponent(
      source,
    )}&key=${encodeURIComponent(key)}`,
  ).then((r) => r.json)

export const loginState = () =>
  httpClient(`${REST_URL}/musicsearch/login/state`).then((r) => r.json)

// ------ 在线试听开关 ------ //

export const previewSetting = (enabled) =>
  httpClient(`${REST_URL}/musicsearch/preview/setting`, {
    method: 'POST',
    body: JSON.stringify({ enabled }),
  }).then((r) => r.json)

// ------ 在线试听：地址生成 + 播放前预检 ------ //

// previewUrl 生成 <audio> 可直接播放的试听地址。
// 为什么这样拼：<audio> 无法携带 Authorization 头，只能用 Navidrome 的 `?jwt=` 查询参数认证
// （与 /api/events 同一机制）；且必须带 baseUrl() 前缀，否则生产环境子路径 /music 下
// 会请求到站根 /api/...，被 nginx 路由到博客后端而返回 JSON。
export const previewUrl = ({ id, source }) => {
  const token = window.localStorage.getItem('token') || ''
  return baseUrl(
    `${REST_URL}/musicsearch/preview?id=${encodeURIComponent(
      id,
    )}&source=${encodeURIComponent(
      source || '',
    )}&jwt=${encodeURIComponent(token)}`,
  )
}

// probePreview 播放前预检。
// 为什么需要：<audio> 元素读不到响应体，后端返回非音频内容时浏览器只会给出
// 「Failed to load because no supported source was found.」这种噪音，用户无法区分
// 「版权/平台风控导致无音源」「试听开关未开」「登录过期」。
// 这里先用 fetch 只取 1 字节（Range: bytes=0-0）拿状态码与后端 error 文本。
export const probePreview = async (url) => {
  const res = await fetch(url, { headers: { Range: 'bytes=0-0' } })
  let error = ''
  if (!res.ok) {
    try {
      const body = await res.json()
      error = (body && body.error) || ''
    } catch (e) {
      // 非 JSON 响应（如网关错误页）没有可用错误文本，保持为空
      error = ''
    }
  }
  // 预检只关心状态码，拿到响应头后立刻丢弃响应体，避免把整首歌下载一遍
  if (res.body && typeof res.body.cancel === 'function') {
    try {
      await res.body.cancel()
    } catch (e) {
      // 取消失败不影响判定结果，忽略
    }
  }
  return { ok: res.ok, status: res.status, error }
}

// previewErrorMessage 把预检结果翻译成用户能看懂的原因。
// 后端会区分「平台风控/无音源」「试听开关未开」「未认证」，而 <audio> 只能给出浏览器噪音。
export const previewErrorMessage = ({ status, error } = {}) => {
  // 401 多为页面久置导致 JWT 过期，后端返回的是通用鉴权文案，对用户没有指导意义
  if (status === 401) return '登录已过期，请重新登录或刷新页面后重试'
  // 音源类失败（502/404）给出去处建议：同名歌曲在其它平台通常可播
  if (error) return status === 502 ? `${error}。可切换其它平台重试` : error
  if (!status) return '音源不可用，可切换其它平台重试'
  return `音源不可用（HTTP ${status}），可切换其它平台重试`
}
