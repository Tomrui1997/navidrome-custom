import React, { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Radio,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
  makeStyles,
} from '@material-ui/core'
import DeleteSweepIcon from '@material-ui/icons/DeleteSweep'
import RefreshIcon from '@material-ui/icons/Refresh'
import { useNotify, useTranslate } from 'react-admin'
import * as api from './api'

const useStyles = makeStyles((theme) => ({
  card: { margin: theme.spacing(2), overflow: 'auto' },
  toolbar: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(2),
    flexWrap: 'wrap',
    marginBottom: theme.spacing(1),
  },
  groupCard: { marginBottom: theme.spacing(2) },
  groupTitle: { display: 'flex', alignItems: 'center', gap: theme.spacing(1) },
  keepRow: { background: theme.palette.action.selected },
  removeRow: { opacity: 0.9 },
  path: { wordBreak: 'break-all', fontFamily: 'monospace', fontSize: 12 },
  dialogList: {
    maxHeight: 320,
    overflow: 'auto',
    fontFamily: 'monospace',
    fontSize: 12,
    background: theme.palette.background.default,
    padding: theme.spacing(1),
  },
}))

// 重复歌曲检测与一键清理页（仅管理员）。
//
// 交互：检测（只读） → 展示分组 → 可手动改选每组保留项 → 一键清理
// 清理前**固定先 dryRun 预览**，把将删除的清单给用户确认后才真正执行；
// 执行时本地文件与网盘文件都是软删（分别进 trash/ 与网盘 /_deleted/），可恢复。
const DuplicateList = () => {
  const classes = useStyles()
  const translate = useTranslate()
  const notify = useNotify()

  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(false)
  const [keepMap, setKeepMap] = useState({})
  const [preview, setPreview] = useState(null) // 服务端校验后的待删清单（确认框）
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState(null)

  const detect = useCallback(async () => {
    setLoading(true)
    setResult(null)
    try {
      const res = await api.detectDuplicates()
      setData(res)
      // 初始化每组、每「内容集群」的保留项（服务端推荐值，用户可再改）。
      // 注意：一组内可能有多个内容集群（如录音室版 + Live 版），每个集群各留一份，
      // 因此必须按 cluster 记录，不能只记 group -> 单个 id，否则会误删其它版本。
      const km = {}
      ;(res.groups || []).forEach((g) => {
        const m = {}
        ;(g.items || []).forEach((it) => {
          if (it.keep) m[it.cluster] = it.id
        })
        km[g.key] = m
      })
      setKeepMap(km)
    } catch (e) {
      notify(`${translate('resources.duplicates.detectFailed')}: ${e.message}`, 'warning')
    } finally {
      setLoading(false)
    }
  }, [notify, translate])

  useEffect(() => {
    detect()
  }, [detect])

  // 待删除项 = 每组内「当前未被保留」的歌曲。
  // 保留判定：用户手动改过按集群选择，否则用服务端推荐（it.keep）。
  const pending = useMemo(() => {
    if (!data) return []
    const out = []
    ;(data.groups || []).forEach((g) => {
      const m = keepMap[g.key]
      ;(g.items || []).forEach((it) => {
        const kept = m ? m[it.cluster] === it.id : it.keep
        if (!kept) out.push({ group: g, item: it })
      })
    })
    return out
  }, [data, keepMap])

  // 点「一键清理」：先让服务端校验（挡掉保留项），再弹确认框展示将要删除的清单
  const handlePreview = async () => {
    if (pending.length === 0) {
      notify(translate('resources.duplicates.none'), 'info')
      return
    }
    setRunning(true)
    try {
      const v = await api.verifyDuplicateIds(pending.map((p) => p.item.id))
      const allowed = v.allowed || []
      const rejected = v.rejected || []
      if (rejected.length > 0) {
        notify(
          translate('resources.duplicates.rejected', { count: rejected.length }),
          'warning',
        )
      }
      if (allowed.length === 0) {
        notify(translate('resources.duplicates.nothingAllowed'), 'warning')
        return
      }
      // 把 id 映射回路径，便于用户在确认框里逐条核对
      const byId = {}
      pending.forEach((p) => {
        byId[p.item.id] = p.item
      })
      setPreview({
        ids: allowed,
        paths: allowed.map((id) => (byId[id] || {}).path || id),
      })
    } catch (e) {
      notify(`${translate('resources.duplicates.cleanFailed')}: ${e.message}`, 'warning')
    } finally {
      setRunning(false)
    }
  }

  const handleConfirm = async () => {
    const ids = preview ? preview.ids : []
    setPreview(null)
    setRunning(true)
    try {
      const res = await api.deleteMediaFiles({
        ids,
        scope: api.CLEAN_SCOPE,
        dryRun: false,
      })
      setResult(res)
      notify(
        translate('resources.duplicates.done', {
          db: res.dbDeleted || 0,
          local: (res.summary && res.summary.local) || 0,
          remote: (res.summary && res.summary.remote) || 0,
        }),
        'info',
      )
      await detect() // 清理后自动重新检测，直接验证结果
    } catch (e) {
      notify(`${translate('resources.duplicates.cleanFailed')}: ${e.message}`, 'warning')
    } finally {
      setRunning(false)
    }
  }

  return (
    <Card className={classes.card}>
      <CardContent>
        <Box className={classes.toolbar}>
          <Typography variant="h6">{translate('resources.duplicates.name')}</Typography>
          <Button
            variant="outlined"
            startIcon={<RefreshIcon />}
            onClick={detect}
            disabled={loading || running}
          >
            {translate('resources.duplicates.detect')}
          </Button>
          <Button
            variant="contained"
            color="secondary"
            startIcon={<DeleteSweepIcon />}
            onClick={handlePreview}
            disabled={loading || running || pending.length === 0}
          >
            {translate('resources.duplicates.clean')}（{pending.length}）
          </Button>
          {loading && <CircularProgress size={20} />}
        </Box>

        {data && (
          <Typography variant="body2" color="textSecondary">
            {translate('resources.duplicates.summary', {
              scanned: data.scanned,
              groups: data.groupCount,
              redundant: data.redundantCount,
            })}
            {data.elapsedMs ? `  ·  ${data.elapsedMs} ms` : ''}
          </Typography>
        )}

        {data && data.groupCount === 0 && (
          <Typography variant="body1" style={{ marginTop: 16 }}>
            {translate('resources.duplicates.none')}
          </Typography>
        )}

        {result && (
          <Typography variant="body2" style={{ marginTop: 8 }}>
            {translate('resources.duplicates.done', {
              db: result.dbDeleted || 0,
              local: (result.summary && result.summary.local) || 0,
              remote: (result.summary && result.summary.remote) || 0,
            })}
          </Typography>
        )}

        {data &&
          (data.groups || []).map((g) => {
            const clusterKeep = keepMap[g.key]
            return (
              <Card key={g.key} variant="outlined" className={classes.groupCard}>
                <CardContent>
                  <Box className={classes.groupTitle}>
                    <Typography variant="subtitle1">
                      {g.namesDiffer
                        ? translate('resources.duplicates.contentDupTitle', {
                            count: g.items.length,
                          })
                        : `${g.artist} — ${g.title}`}
                    </Typography>
                    {g.namesDiffer && (
                      <Chip
                        size="small"
                        color="secondary"
                        label={translate('resources.duplicates.namesDiffer')}
                      />
                    )}
                    <Chip
                      size="small"
                      color={g.sameContent ? 'default' : 'secondary'}
                      label={
                        g.sameContent
                          ? translate('resources.duplicates.sameContent')
                          : translate('resources.duplicates.diffContent')
                      }
                    />
                    <Typography variant="caption" color="textSecondary">
                      {translate('resources.duplicates.groupCount', { count: g.items.length })}
                    </Typography>
                  </Box>

                  <Table size="small">
                    <TableHead>
                      <TableRow>
                        <TableCell padding="checkbox" />
                        <TableCell>{translate('resources.duplicates.colPath')}</TableCell>
                        <TableCell>{translate('resources.duplicates.colSize')}</TableCell>
                        <TableCell>{translate('resources.duplicates.colBitrate')}</TableCell>
                        <TableCell>{translate('resources.duplicates.colFormat')}</TableCell>
                        <TableCell>{translate('resources.duplicates.colState')}</TableCell>
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {g.items.map((it) => {
                        // 保留判定：用户手动改过按集群选择，否则用服务端推荐
                        const isKeep = clusterKeep
                          ? clusterKeep[it.cluster] === it.id
                          : it.keep
                        return (
                          <TableRow
                            key={it.id}
                            className={isKeep ? classes.keepRow : classes.removeRow}
                          >
                            <TableCell padding="checkbox">
                              <Radio
                                size="small"
                                checked={isKeep}
                                onChange={() =>
                                  setKeepMap((m) => ({
                                    ...m,
                                    [g.key]: { ...(m[g.key] || {}), [it.cluster]: it.id },
                                  }))
                                }
                                inputProps={{ 'aria-label': 'keep' }}
                              />
                            </TableCell>
                            <TableCell className={classes.path}>
                              {it.path}
                              {!it.exists && (
                                <span style={{ color: '#f44336' }}>
                                  {' '}
                                  ({translate('resources.duplicates.missing')})
                                </span>
                              )}
                            </TableCell>
                            <TableCell>{(it.size / 1048576).toFixed(2)} MB</TableCell>
                            <TableCell>{it.bitrate} kbps</TableCell>
                            <TableCell>{it.format}</TableCell>
                            <TableCell>
                              <Chip
                                size="small"
                                color={isKeep ? 'primary' : 'default'}
                                label={
                                  isKeep
                                    ? translate('resources.duplicates.keep')
                                    : translate('resources.duplicates.remove')
                                }
                              />
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            )
          })}
      </CardContent>

      {/* 执行前预览：把将删除的清单原文给用户看一遍 */}
      <Dialog open={!!preview} onClose={() => setPreview(null)} maxWidth="md" fullWidth>
        <DialogTitle>{translate('resources.duplicates.confirmTitle')}</DialogTitle>
        <DialogContent>
          <Typography variant="body2" gutterBottom>
            {translate('resources.duplicates.confirmBody', {
              count: (preview && preview.ids.length) || 0,
            })}
          </Typography>
          <div className={classes.dialogList}>
            {((preview && preview.paths) || []).map((p) => (
              <div key={p}>· {p}</div>
            ))}
          </div>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPreview(null)}>
            {translate('resources.duplicates.cancel')}
          </Button>
          <Button color="secondary" variant="contained" onClick={handleConfirm}>
            {translate('resources.duplicates.execute')}
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  )
}

export default DuplicateList
