import httpClient from '../dataProvider/httpClient'
import { REST_URL } from '../consts'

// 与后端新增的原生接口对接（均为管理员专用）。
// 说明：httpClient 在非 2xx 时会抛出 HttpError（body 里带 error 字段），调用方需捕获。

export const syncStatus = () =>
  httpClient(`${REST_URL}/baidusync/status`).then((r) => r.json)

export const syncStart = (mode = 'incremental', types) =>
  httpClient(`${REST_URL}/baidusync/sync`, {
    method: 'POST',
    body: JSON.stringify({ mode, types }),
  }).then((r) => r.json)

export const syncCancel = () =>
  httpClient(`${REST_URL}/baidusync/cancel`, { method: 'POST' }).then(
    (r) => r.json,
  )

export const syncConfigGet = () =>
  httpClient(`${REST_URL}/baidusync/config`).then((r) => r.json)

export const syncConfigSet = (cfg) =>
  httpClient(`${REST_URL}/baidusync/config`, {
    method: 'PUT',
    body: JSON.stringify(cfg),
  }).then((r) => r.json)

export const syncLog = (lines = 200, date = '') =>
  httpClient(
    `${REST_URL}/baidusync/log?lines=${lines}${date ? `&date=${date}` : ''}`,
  ).then((r) => r.json)

export const mediaDelete = ({ ids, scope, soft = true, dryRun = false }) =>
  httpClient(`${REST_URL}/mediaops/delete`, {
    method: 'POST',
    body: JSON.stringify({ ids, scope, soft, dryRun }),
  }).then((r) => r.json)

export const trashList = () =>
  httpClient(`${REST_URL}/mediaops/trash`).then((r) => r.json)

export const trashRestore = (ids) =>
  httpClient(`${REST_URL}/mediaops/restore`, {
    method: 'POST',
    body: JSON.stringify({ ids }),
  }).then((r) => r.json)

export const trashPurge = (ids, deleteRemote = false) =>
  httpClient(`${REST_URL}/mediaops/purge`, {
    method: 'POST',
    body: JSON.stringify({ ids, deleteRemote }),
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
