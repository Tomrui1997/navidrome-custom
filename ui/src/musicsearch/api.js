import httpClient from '../dataProvider/httpClient'
import { REST_URL } from '../consts'

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
