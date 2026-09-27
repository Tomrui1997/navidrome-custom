import React, { useCallback, useEffect, useState } from 'react'
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Divider,
  FormControlLabel,
  Grid,
  MenuItem,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import { Title, useNotify, usePermissions, useTranslate } from 'react-admin'
import RefreshIcon from '@material-ui/icons/Refresh'
import PlayArrowIcon from '@material-ui/icons/PlayArrow'
import StopIcon from '@material-ui/icons/Stop'
import RestoreIcon from '@material-ui/icons/Restore'
import DeleteForeverIcon from '@material-ui/icons/DeleteForever'
import * as api from './api'

const useStyles = makeStyles((theme) => ({
  root: { marginTop: '1em', paddingBottom: '1em' },
  section: { marginTop: theme.spacing(2) },
  log: {
    maxHeight: 320,
    overflow: 'auto',
    background: theme.palette.type === 'dark' ? '#111' : '#f5f5f5',
    padding: theme.spacing(1),
    fontFamily: 'monospace',
    fontSize: '0.75rem',
    whiteSpace: 'pre-wrap',
    margin: 0,
  },
  row: { display: 'flex', alignItems: 'center', gap: theme.spacing(1) },
  logDate: { minWidth: 160, marginLeft: theme.spacing(2) },
  statusChip: { marginRight: theme.spacing(1) },
}))

// toForm / fromForm：在「配置对象」与「表单字段」之间转换（数字与定时子对象需要拆平）。
const toForm = (c) => ({
  enabled: c.enabled,
  baidupcsBin: c.baidupcsBin,
  remoteRoot: c.remoteRoot,
  localRoot: c.localRoot,
  includeLyrics: c.includeLyrics,
  includeCovers: c.includeCovers,
  threads: String(c.threads),
  parallelFiles: String(c.parallelFiles),
  // 排除目录用逗号分隔展示；未配置时给出默认提示
  excludeDirs: (c.excludeDirs || []).join(', '),
  lockPaths: c.lockPaths !== false,
  softDelete: c.softDelete,
  deleteExtra: c.deleteExtra,
  trashDir: c.trashDir,
  remoteTrashDir: c.remoteTrashDir,
  retentionDays: String(c.retentionDays),
  logRetentionDays: String(c.logRetentionDays || 7),
  scheduleEnabled: c.schedule && c.schedule.enabled,
  scheduleMode: (c.schedule && c.schedule.mode) || 'daily',
  scheduleAt: (c.schedule && c.schedule.at) || '03:00',
  scheduleMinutes: String((c.schedule && c.schedule.minutes) || 360),
})

const fromForm = (f) => ({
  enabled: f.enabled,
  baidupcsBin: f.baidupcsBin,
  remoteRoot: f.remoteRoot,
  localRoot: f.localRoot,
  includeLyrics: f.includeLyrics,
  includeCovers: f.includeCovers,
  threads: parseInt(f.threads, 10) || 1,
  parallelFiles: parseInt(f.parallelFiles, 10) || 1,
  // 逗号（中文逗号也支持）分隔 → 数组，去空值与空白
  excludeDirs: String(f.excludeDirs || '')
    .split(/[,，]/)
    .map((s) => s.trim().replace(/^\/+|\/+$/g, ''))
    .filter(Boolean),
  lockPaths: !!f.lockPaths,
  softDelete: f.softDelete,
  deleteExtra: f.deleteExtra,
  trashDir: f.trashDir,
  remoteTrashDir: f.remoteTrashDir,
  retentionDays: parseInt(f.retentionDays, 10) || 0,
  logRetentionDays: parseInt(f.logRetentionDays, 10) || 0,
  schedule: {
    enabled: f.scheduleEnabled,
    mode: f.scheduleMode,
    at: f.scheduleAt,
    minutes: parseInt(f.scheduleMinutes, 10) || 360,
  },
})

const fmtTime = (v) => {
  if (!v) return '-'
  const d = new Date(v)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1970) return '-'
  return d.toLocaleString()
}

// Sync 同步管理页：状态面板 + 手动同步 + 配置 + 日志 + 回收站（仅管理员）。
const Sync = () => {
  const classes = useStyles()
  const notify = useNotify()
  const translate = useTranslate()
  const { permissions } = usePermissions()

  const [loading, setLoading] = useState(true)
  const [status, setStatus] = useState(null)
  const [form, setForm] = useState(null)
  const [logLines, setLogLines] = useState([])
  // 日志按天分文件：记录当前查看的日期与可选日期列表
  const [logDate, setLogDate] = useState('')
  const [logFiles, setLogFiles] = useState([])
  const [trash, setTrash] = useState([])
  const [saving, setSaving] = useState(false)
  const [busy, setBusy] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(true)

  const load = useCallback(
    async ({ withLog = true } = {}) => {
      try {
        const st = await api.syncStatus()
        setStatus(st)
        if (!form && st.config) setForm(toForm(st.config))
        if (withLog) {
          const l = await api.syncLog(200, logDate)
          setLogLines(l.lines || [])
          setLogFiles(l.files || [])
          if (l.date) setLogDate(l.date)
        }
        const t = await api.trashList()
        setTrash(t.entries || [])
      } catch (err) {
        notify(api.errMessage(err), { type: 'warning' })
      } finally {
        setLoading(false)
      }
    },
    [form, notify, logDate],
  )

  useEffect(() => {
    load()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!autoRefresh) return undefined
    const timer = setInterval(() => load({ withLog: true }), 5000)
    return () => clearInterval(timer)
  }, [autoRefresh, load])

  if (permissions !== 'admin') {
    return (
      <Card className={classes.root}>
        <Title title={'Navidrome - ' + translate('menu.sync.name')} />
        <CardContent>
          <Typography variant="body1">
            {translate('resources.sync.errors.adminOnly')}
          </Typography>
        </CardContent>
      </Card>
    )
  }

  const setField = (name) => (e) => {
    const value =
      e && e.target && typeof e.target.type !== 'undefined'
        ? e.target.type === 'checkbox'
          ? e.target.checked
          : e.target.value
        : e
    setForm((prev) => ({ ...prev, [name]: value }))
  }

  const handleSync = async (mode) => {
    setBusy(true)
    try {
      await api.syncStart(mode)
      notify(translate('resources.sync.notifications.started'), {
        type: 'info',
      })
      await load()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    } finally {
      setBusy(false)
    }
  }

  const handleCancel = async () => {
    setBusy(true)
    try {
      await api.syncCancel()
      notify(translate('resources.sync.notifications.cancelled'), {
        type: 'info',
      })
      await load()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    } finally {
      setBusy(false)
    }
  }

  const handleSave = async () => {
    setSaving(true)
    try {
      const saved = await api.syncConfigSet(fromForm(form))
      setForm(toForm(saved))
      notify(translate('resources.sync.notifications.saved'), { type: 'info' })
      await load()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    } finally {
      setSaving(false)
    }
  }

  const handleRestore = async (id) => {
    try {
      await api.trashRestore([id])
      notify(translate('resources.sync.notifications.restored'), {
        type: 'info',
      })
      await load()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    }
  }

  const handlePurge = async (id) => {
    try {
      await api.trashPurge([id], false)
      notify(translate('resources.sync.notifications.purged'), { type: 'info' })
      await load()
    } catch (err) {
      notify(api.errMessage(err), { type: 'warning' })
    }
  }

  if (loading && !status) {
    return (
      <Card className={classes.root}>
        <Title title={'Navidrome - ' + translate('menu.sync.name')} />
        <CardContent className={classes.row}>
          <CircularProgress size={20} />
          <Typography variant="body2">{translate('ra.page.loading')}</Typography>
        </CardContent>
      </Card>
    )
  }

  const task = (status && status.task) || {}
  const login = (status && status.login) || {}

  return (
    <Card className={classes.root}>
      <Title title={'Navidrome - ' + translate('menu.sync.name')} />
      <CardHeader
        title={translate('resources.sync.title')}
        subheader={translate('resources.sync.subtitle')}
        action={
          <Button
            startIcon={<RefreshIcon />}
            onClick={() => load()}
            size="small"
          >
            {translate('ra.action.refresh')}
          </Button>
        }
      />
      <CardContent>
        {/* ---------- 状态 ---------- */}
        <div className={classes.row}>
          <Chip
            className={classes.statusChip}
            size="small"
            color={login.loggedIn ? 'primary' : 'secondary'}
            label={
              login.loggedIn
                ? `${translate('resources.sync.login.loggedIn')}: ${login.userName || login.uid}`
                : translate('resources.sync.login.notLoggedIn')
            }
          />
          <Chip
            className={classes.statusChip}
            size="small"
            variant="outlined"
            label={
              task.running
                ? `${translate('resources.sync.task.running')} · ${task.filesDone || 0}/${task.filesSeen || 0}`
                : translate('resources.sync.task.idle')
            }
          />
          <Chip
            className={classes.statusChip}
            size="small"
            variant="outlined"
            label={`${translate('resources.sync.lastSync')}: ${fmtTime(status && status.lastSync)}${
              status && status.lastSyncMsg ? ` (${status.lastSyncMsg})` : ''
            }`}
          />
          <Chip
            size="small"
            variant="outlined"
            label={`${translate('resources.sync.nextRun')}: ${fmtTime(status && status.nextRun)}`}
          />
        </div>

        <div className={classes.section + ' ' + classes.row}>
          <Button
            variant="contained"
            color="primary"
            startIcon={<PlayArrowIcon />}
            disabled={busy || task.running}
            onClick={() => handleSync('incremental')}
          >
            {translate('resources.sync.actions.syncNow')}
          </Button>
          <Button
            variant="outlined"
            disabled={busy || task.running}
            onClick={() => handleSync('full')}
          >
            {translate('resources.sync.actions.syncFull')}
          </Button>
          <Button
            variant="outlined"
            color="secondary"
            startIcon={<StopIcon />}
            disabled={busy || !task.running}
            onClick={handleCancel}
          >
            {translate('resources.sync.actions.cancel')}
          </Button>
          <FormControlLabel
            control={
              <Switch
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
            }
            label={translate('resources.sync.actions.autoRefresh')}
          />
        </div>

        <Divider className={classes.section} />

        {/* ---------- 配置 ---------- */}
        <Typography variant="h6" className={classes.section}>
          {translate('resources.sync.config.title')}
        </Typography>
        {form && (
          <Grid container spacing={2} className={classes.section}>
            <Grid item xs={12}>
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.enabled}
                    onChange={setField('enabled')}
                  />
                }
                label={translate('resources.sync.config.enabled')}
              />
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.includeLyrics}
                    onChange={setField('includeLyrics')}
                  />
                }
                label={translate('resources.sync.config.includeLyrics')}
              />
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.includeCovers}
                    onChange={setField('includeCovers')}
                  />
                }
                label={translate('resources.sync.config.includeCovers')}
              />
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.softDelete}
                    onChange={setField('softDelete')}
                  />
                }
                label={translate('resources.sync.config.softDelete')}
              />
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.lockPaths}
                    onChange={setField('lockPaths')}
                  />
                }
                label={translate('resources.sync.config.lockPaths')}
              />
            </Grid>
            <Grid item xs={12} md={6}>
              <TextField
                fullWidth
                variant="outlined"
                disabled={form.lockPaths}
                helperText={
                  form.lockPaths
                    ? translate('resources.sync.config.pathLockedHint')
                    : undefined
                }
                label={translate('resources.sync.config.remoteRoot')}
                value={form.remoteRoot}
                onChange={setField('remoteRoot')}
              />
            </Grid>
            <Grid item xs={12} md={6}>
              <TextField
                fullWidth
                variant="outlined"
                disabled={form.lockPaths}
                helperText={
                  form.lockPaths
                    ? translate('resources.sync.config.pathLockedHint')
                    : undefined
                }
                label={translate('resources.sync.config.localRoot')}
                value={form.localRoot}
                onChange={setField('localRoot')}
              />
            </Grid>
            <Grid item xs={12} md={6}>
              <TextField
                fullWidth
                variant="outlined"
                label={translate('resources.sync.config.excludeDirs')}
                helperText={translate('resources.sync.config.excludeDirsHint')}
                value={form.excludeDirs}
                onChange={setField('excludeDirs')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                type="number"
                label={translate('resources.sync.config.threads')}
                value={form.threads}
                onChange={setField('threads')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                type="number"
                label={translate('resources.sync.config.parallelFiles')}
                value={form.parallelFiles}
                onChange={setField('parallelFiles')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                type="number"
                label={translate('resources.sync.config.retentionDays')}
                value={form.retentionDays}
                onChange={setField('retentionDays')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                type="number"
                label={translate('resources.sync.config.logRetentionDays')}
                helperText={translate('resources.sync.config.logRetentionDaysHint')}
                value={form.logRetentionDays}
                onChange={setField('logRetentionDays')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                label={translate('resources.sync.config.remoteTrashDir')}
                value={form.remoteTrashDir}
                onChange={setField('remoteTrashDir')}
              />
            </Grid>

            <Grid item xs={12}>
              <FormControlLabel
                control={
                  <Switch
                    checked={!!form.scheduleEnabled}
                    onChange={setField('scheduleEnabled')}
                  />
                }
                label={translate('resources.sync.config.scheduleEnabled')}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                select
                fullWidth
                variant="outlined"
                label={translate('resources.sync.config.scheduleMode')}
                value={form.scheduleMode}
                onChange={setField('scheduleMode')}
                disabled={!form.scheduleEnabled}
              >
                <MenuItem value="daily">
                  {translate('resources.sync.config.modeDaily')}
                </MenuItem>
                <MenuItem value="interval">
                  {translate('resources.sync.config.modeInterval')}
                </MenuItem>
              </TextField>
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                label={translate('resources.sync.config.scheduleAt')}
                placeholder="03:00"
                value={form.scheduleAt}
                onChange={setField('scheduleAt')}
                disabled={!form.scheduleEnabled || form.scheduleMode !== 'daily'}
              />
            </Grid>
            <Grid item xs={6} md={3}>
              <TextField
                fullWidth
                variant="outlined"
                type="number"
                label={translate('resources.sync.config.scheduleMinutes')}
                value={form.scheduleMinutes}
                onChange={setField('scheduleMinutes')}
                disabled={!form.scheduleEnabled || form.scheduleMode !== 'interval'}
              />
            </Grid>
            <Grid item xs={12} className={classes.row}>
              <Button
                variant="contained"
                color="primary"
                disabled={saving}
                onClick={handleSave}
              >
                {translate('ra.action.save')}
              </Button>
            </Grid>
          </Grid>
        )}

        <Divider className={classes.section} />

        {/* ---------- 日志 ---------- */}
        <div className={classes.row}>
          <Typography variant="h6" className={classes.section}>
            {translate('resources.sync.log.title')}
          </Typography>
          {/* 日志按天分文件，可按日期回看（避免单文件过大打不开） */}
          {logFiles.length > 1 && (
            <TextField
              select
              size="small"
              variant="outlined"
              label={translate('resources.sync.log.date')}
              value={logDate}
              onChange={(e) => setLogDate(e.target.value)}
              className={classes.logDate}
            >
              {logFiles.map((d) => (
                <MenuItem key={d} value={d}>
                  {d}
                </MenuItem>
              ))}
            </TextField>
          )}
        </div>
        <pre className={classes.log}>
          {logLines.length
            ? logLines.join('\n')
            : translate('resources.sync.log.empty')}
        </pre>

        <Divider className={classes.section} />

        {/* ---------- 回收站 ---------- */}
        <Typography variant="h6" className={classes.section}>
          {translate('resources.sync.trash.title')} ({trash.length})
        </Typography>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>{translate('resources.sync.trash.titleField')}</TableCell>
              <TableCell>{translate('resources.sync.trash.path')}</TableCell>
              <TableCell>{translate('resources.sync.trash.deletedAt')}</TableCell>
              <TableCell>{translate('resources.sync.trash.deletedBy')}</TableCell>
              <TableCell align="right">
                {translate('resources.sync.trash.actions')}
              </TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {trash.slice(0, 100).map((e) => (
              <TableRow key={e.id}>
                <TableCell>{e.title}</TableCell>
                <TableCell>{e.relPath}</TableCell>
                <TableCell>{fmtTime(e.deletedAt)}</TableCell>
                <TableCell>{e.deletedBy}</TableCell>
                <TableCell align="right">
                  <Button
                    size="small"
                    startIcon={<RestoreIcon />}
                    onClick={() => handleRestore(e.id)}
                  >
                    {translate('resources.sync.trash.restore')}
                  </Button>
                  <Button
                    size="small"
                    color="secondary"
                    startIcon={<DeleteForeverIcon />}
                    onClick={() => handlePurge(e.id)}
                  >
                    {translate('resources.sync.trash.purge')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {trash.length === 0 && (
              <TableRow>
                <TableCell colSpan={5}>
                  <Typography variant="body2">
                    {translate('resources.sync.trash.empty')}
                  </Typography>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

export default Sync
