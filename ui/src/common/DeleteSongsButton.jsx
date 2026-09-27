import React, { useEffect, useState } from 'react'
import {
  Button as MuiButton,
  Checkbox,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  List,
  ListItem,
  ListItemText,
  Typography,
} from '@material-ui/core'
import { alpha, makeStyles } from '@material-ui/core/styles'
import DeleteIcon from '@material-ui/icons/Delete'
import clsx from 'clsx'
import {
  Button,
  useNotify,
  usePermissions,
  useRefresh,
  useTranslate,
  useUnselectAll,
} from 'react-admin'
import * as api from '../syncing/api'

const useStyles = makeStyles(
  (theme) => ({
    deleteButton: {
      color: theme.palette.error.main,
      '&:hover': {
        backgroundColor: alpha(theme.palette.error.main, 0.12),
      },
    },
    scopeRow: { display: 'flex', flexDirection: 'column' },
    preview: {
      maxHeight: 220,
      overflow: 'auto',
      background: theme.palette.type === 'dark' ? '#111' : '#f5f5f5',
      padding: theme.spacing(1),
      marginTop: theme.spacing(1),
    },
    hint: { marginTop: theme.spacing(1) },
  }),
  { name: 'NdDeleteSongsButton' },
)

// DeleteSongsButton 歌曲列表批量删除按钮：
//   1) 打开时先做 dry-run 预演，展示将受影响的曲目；
//   2) 用户勾选删除范围（库记录 / 本地文件 / 网盘文件，默认前两项）；
//   3) 确认后执行（软删除优先，可在「同步」页的回收站里恢复）。
const DeleteSongsButton = ({ selectedIds = [], className }) => {
  const classes = useStyles()
  const notify = useNotify()
  const translate = useTranslate()
  const refresh = useRefresh()
  const unselectAll = useUnselectAll()
  const { permissions } = usePermissions()

  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState(null)
  const [scope, setScope] = useState({
    library: true,
    local: true,
    remote: false,
  })

  // 打开对话框或勾选范围变化时重新预演（勾选“删除网盘文件”后才能看到网盘目标路径）。
  // 注意：必须在任何 return 之前调用，遵守 Hooks 调用顺序。
  useEffect(() => {
    if (!open) return undefined
    let cancelled = false
    setBusy(true)
    api
      .mediaDelete({ ids: selectedIds, scope, soft: true, dryRun: true })
      .then((res) => {
        if (!cancelled) setPreview(res)
      })
      .catch((err) => {
        if (!cancelled) notify(api.errMessage(err), { type: 'warning' })
      })
      .finally(() => {
        if (!cancelled) setBusy(false)
      })
    return () => {
      cancelled = true
    }
    // selectedIds 用长度代替，避免数组引用变化导致重复请求
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, scope.library, scope.local, scope.remote, selectedIds.length])

  if (permissions !== 'admin' || selectedIds.length === 0) {
    return null
  }

  const handleOpen = () => {
    setOpen(true)
    setPreview(null)
  }

  const handleClose = () => {
    setOpen(false)
    setPreview(null)
  }

  const handleConfirm = async () => {
    setBusy(true)
    try {
      const res = await api.mediaDelete({
        ids: selectedIds,
        scope,
        soft: true,
        dryRun: false,
      })
      notify(
        translate('resources.sync.notifications.deleted', {
          count: selectedIds.length,
          db: res.dbDeleted || 0,
        }),
        { type: 'info' },
      )
      refresh()
      unselectAll('song')
      handleClose()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    } finally {
      setBusy(false)
    }
  }

  const toggleScope = (name) => (e) =>
    setScope((prev) => ({ ...prev, [name]: e.target.checked }))

  const results = (preview && preview.results) || []
  const caption = translate('ra.action.delete')
  // 状态码本地化（预演/执行/跳过/失败）
  const statusText = (s) =>
    translate(`resources.sync.deleteDialog.status.${s}`, { _: s })
  const previewLine = (r) =>
    `${r.title || r.id} — ${translate(
      'resources.sync.deleteDialog.localLabel',
    )}:${statusText(r.local)} ${translate(
      'resources.sync.deleteDialog.remoteLabel',
    )}:${statusText(r.remote)}${r.remoteTarget ? ` → ${r.remoteTarget}` : ''}${
      r.reason ? ` (${r.reason})` : ''
    }`

  return (
    <>
      <Button
        onClick={handleOpen}
        aria-label={caption}
        label={caption}
        className={clsx('ra-delete-button', classes.deleteButton, className)}
      >
        <DeleteIcon />
      </Button>
      <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
        <DialogTitle>
          {translate('resources.sync.deleteDialog.title', {
            count: selectedIds.length,
          })}
        </DialogTitle>
        <DialogContent>
          <div className={classes.scopeRow}>
            <FormControlLabel
              control={
                <Checkbox
                  checked={scope.library}
                  onChange={toggleScope('library')}
                />
              }
              label={translate('resources.sync.deleteDialog.scopeLibrary')}
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={scope.local}
                  onChange={toggleScope('local')}
                />
              }
              label={translate('resources.sync.deleteDialog.scopeLocal')}
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={scope.remote}
                  onChange={toggleScope('remote')}
                />
              }
              label={translate('resources.sync.deleteDialog.scopeRemote')}
            />
          </div>

          <Typography variant="caption" className={classes.hint}>
            {translate('resources.sync.deleteDialog.softHint')}
          </Typography>

          <div className={classes.preview}>
            {busy && !preview && <CircularProgress size={18} />}
            {results.slice(0, 15).map((r) => (
              <Typography variant="caption" display="block" key={r.id}>
                {previewLine(r)}
              </Typography>
            ))}
            {results.length > 15 && (
              <Typography variant="caption" display="block">
                {`… ${translate('resources.sync.deleteDialog.more', {
                  count: results.length - 15,
                })}`}
              </Typography>
            )}
            {preview && preview.summary && (
              <List dense>
                <ListItem>
                  <ListItemText
                    primary={translate('resources.sync.deleteDialog.summary', {
                      total: preview.summary.total || 0,
                    })}
                  />
                </ListItem>
              </List>
            )}
          </div>
        </DialogContent>
        <DialogActions>
          <MuiButton onClick={handleClose} disabled={busy}>
            {translate('ra.action.cancel')}
          </MuiButton>
          <MuiButton
            onClick={handleConfirm}
            color="secondary"
            variant="contained"
            disabled={busy || (!scope.library && !scope.local && !scope.remote)}
          >
            {translate('ra.action.delete')}
          </MuiButton>
        </DialogActions>
      </Dialog>
    </>
  )
}

export default DeleteSongsButton
