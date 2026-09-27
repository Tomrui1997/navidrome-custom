import httpClient from '../dataProvider/httpClient'
import { REST_URL } from '../consts'

// 重复歌曲检测与清理：检测为只读接口；清理复用媒体库删除接口
// （该接口已支持 dryRun 预演、软删兜底与网盘路径映射）。
// 注意：全部经 httpClient，无需手动包 baseUrl()。

export const detectDuplicates = () =>
  httpClient(`${REST_URL}/duplicates`).then((r) => r.json)

export const deleteMediaFiles = ({ ids, scope, dryRun = false }) =>
  httpClient(`${REST_URL}/mediaops/delete`, {
    method: 'POST',
    body: JSON.stringify({ ids, scope, dryRun }),
  }).then((r) => r.json)

// 清理前先让服务端校验一遍：把「每组 / 每集群保留项」挡掉，只拿回 allowed 列表
export const verifyDuplicateIds = (ids) =>
  httpClient(`${REST_URL}/duplicates/verify`, {
    method: 'POST',
    body: JSON.stringify({ ids }),
  }).then((r) => r.json)

// 清理固定使用「库记录 + 本地文件 + 网盘」三处同清，与用户确认的策略一致
export const CLEAN_SCOPE = { library: true, local: true, remote: true }
