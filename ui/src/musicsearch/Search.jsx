import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  FormControl,
  FormControlLabel,
  IconButton,
  InputLabel,
  LinearProgress,
  MenuItem,
  Paper,
  Select,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import { useNotify, useTranslate } from 'react-admin'
import { MdPlayArrow, MdStop } from 'react-icons/md'
import { REST_URL } from '../consts'
import * as api from './api'
import LoginDialog from './LoginDialog'

// 常用搜索平台（需与后端白名单一致；QQ 在服务端可用，无需 Cookie）
const SOURCE_OPTIONS = [
  { id: 'netease', name: '网易云' },
  { id: 'qq', name: 'QQ音乐' },
  { id: 'kugou', name: '酷狗' },
  { id: 'kuwo', name: '酷我' },
  { id: 'migu', name: '咪咕' },
]

// 下载内容类型（与助手配置的 downloadTypes 对应）
const TYPE_OPTIONS = [
  { id: 'audio', label: '音频' },
  { id: 'lyric', label: '歌词(.lrc)' },
  { id: 'cover', label: '封面' },
]

const useStyles = makeStyles((theme) => ({
  root: { padding: theme.spacing(2), maxWidth: 1200, margin: '0 auto' },
  header: { display: 'flex', alignItems: 'center', gap: theme.spacing(1) },
  bar: {
    display: 'flex',
    flexWrap: 'wrap',
    alignItems: 'center',
    gap: theme.spacing(1),
    marginTop: theme.spacing(2),
  },
  keyword: { minWidth: 320, flex: '1 1 320px' },
  sources: {
    display: 'flex',
    flexWrap: 'wrap',
    alignItems: 'center',
    gap: theme.spacing(0.5),
    marginTop: theme.spacing(1),
  },
  table: { marginTop: theme.spacing(2) },
  head: { fontWeight: 600 },
  progressBox: { marginTop: theme.spacing(2) },
  footer: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(2),
    marginTop: theme.spacing(2),
  },
  errList: {
    margin: 0,
    paddingLeft: theme.spacing(2),
    color: theme.palette.error.main,
    fontSize: 12,
  },
}))

// 秒 → mm:ss
const fmtDuration = (sec) => {
  if (!sec || sec <= 0) return '-'
  const m = Math.floor(sec / 60)
  const s = Math.floor(sec % 60)
  return `${m}:${String(s).padStart(2, '0')}`
}

// 字节 → MB
const fmtSize = (bytes) => {
  if (!bytes || bytes <= 0) return '-'
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

// 唯一键：同一首歌可能在不同平台都存在
const itemKey = (it) => `${it.source || ''}:${it.id}`

// go-music-api 返回 { code, msg, data: { songs, albums, playlists } }
const normalizeResults = (res, searchType) => {
  const data = (res && res.data) || {}
  if (searchType === 'album') return (data.albums || []).map((x) => ({ ...x, __kind: 'album' }))
  if (searchType === 'playlist') return (data.playlists || []).map((x) => ({ ...x, __kind: 'playlist' }))
  return (data.songs || []).map((x) => ({ ...x, __kind: 'song' }))
}

// Search 在线音乐搜索与下载入库（仅管理员：路由挂载在后端 adminOnly 组内）。
const Search = () => {
  const translate = useTranslate()
  const notify = useNotify()
  const classes = useStyles()

  const [keyword, setKeyword] = useState('')
  const [searchType, setSearchType] = useState('song')
  const [sources, setSources] = useState(['netease', 'kugou', 'kuwo', 'migu'])
  const [health, setHealth] = useState(null)
  const [results, setResults] = useState([])
  const [searching, setSearching] = useState(false)
  const [selected, setSelected] = useState({})
  const [downloadTypes, setDownloadTypes] = useState(['audio', 'lyric', 'cover'])
  const [task, setTask] = useState(null)
  const [loginOpen, setLoginOpen] = useState(false)
  const [previewOn, setPreviewOn] = useState(false)
  const [playingKey, setPlayingKey] = useState('')
  const audioRef = useRef(null)

  // 加载服务可用性
  const loadHealth = useCallback(async () => {
    try {
      setHealth(await api.searchHealth())
    } catch (e) {
      setHealth({ enabled: false, reachable: false, detail: api.errMessage(e) })
    }
  }, [])

  useEffect(() => {
    loadHealth()
  }, [loadHealth])

  // 同步试听开关状态（开关值持久化在助手配置里）
  useEffect(() => {
    if (health) setPreviewOn(Boolean(health.preview))
  }, [health])

  // 轮询下载进度（仅服务可用时）
  useEffect(() => {
    if (!health?.reachable) return undefined
    let alive = true
    const tick = async () => {
      try {
        const t = await api.downloadStatus()
        if (alive) setTask(t)
      } catch (e) {
        // 轮询失败静默处理，避免持续弹窗
      }
    }
    tick()
    const timer = setInterval(tick, 3000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [health])

  const handleSearch = useCallback(async () => {
    const q = keyword.trim()
    if (!q) {
      notify(translate('resources.musicsearch.errors.emptyKeyword'), 'warning')
      return
    }
    if (sources.length === 0) {
      notify('请至少选择一个搜索平台', 'warning')
      return
    }
    setSearching(true)
    try {
      const res = await api.searchMusic({ q, type: searchType, sources: sources.join(',') })
      const list = normalizeResults(res, searchType)
      setResults(list)
      setSelected({})
      notify(translate('resources.musicsearch.notifications.searched', { count: list.length }), 'info')
    } catch (e) {
      notify(api.errMessage(e), 'error')
    } finally {
      setSearching(false)
    }
  }, [keyword, sources, searchType, notify, translate])

  const toggleSource = (id) => {
    setSources((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  }

  const toggleType = (id) => {
    setDownloadTypes((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  }

  const toggleSelect = (it) => {
    const key = itemKey(it)
    setSelected((prev) => {
      const next = { ...prev }
      if (next[key]) delete next[key]
      else next[key] = it
      return next
    })
  }

  const toggleSelectAll = () => {
    if (Object.keys(selected).length === results.length) {
      setSelected({})
      return
    }
    const next = {}
    results.forEach((it) => {
      next[itemKey(it)] = it
    })
    setSelected(next)
  }

  const handleDownload = async () => {
    const items = Object.values(selected)
    if (items.length === 0) {
      notify(translate('resources.musicsearch.errors.noSelection'), 'warning')
      return
    }
    if (downloadTypes.length === 0) {
      notify('请至少选择一种下载内容', 'warning')
      return
    }
    try {
      await api.startDownload({
        items: items.map((it) => ({
          id: String(it.id),
          source: it.source || '',
          name: it.name || '',
          artist: it.artist || '',
          album: it.album || '',
          duration: Number(it.duration) || 0,
          ext: it.ext || '',
          coverUrl: it.cover || '',
        })),
        types: downloadTypes,
      })
      notify(translate('resources.musicsearch.notifications.started'), 'info')
    } catch (e) {
      notify(api.errMessage(e), 'error')
    }
  }

  const handleCancel = async () => {
    try {
      await api.cancelDownload()
      notify(translate('resources.musicsearch.notifications.cancelled'), 'info')
    } catch (e) {
      notify(api.errMessage(e), 'error')
    }
  }

  const selectedCount = Object.keys(selected).length
  const allSelected = results.length > 0 && selectedCount === results.length
  const running = Boolean(task && task.running)
  const canDownload = searchType === 'song'

  // 试听：<audio> 无法携带 Authorization 头，改用 Navidrome 的 ?jwt= 查询参数认证（与 /api/events 同一机制）
  const togglePreview = useCallback(
    (it) => {
      const audio = audioRef.current
      if (!audio) return
      const key = itemKey(it)
      if (playingKey === key) {
        audio.pause()
        setPlayingKey('')
        return
      }
      const token = window.localStorage.getItem('token') || ''
      audio.src = `${REST_URL}/musicsearch/preview?id=${encodeURIComponent(
        it.id,
      )}&source=${encodeURIComponent(it.source || '')}&jwt=${encodeURIComponent(token)}`
      audio.play().then(
        () => setPlayingKey(key),
        (e) => {
          notify(
            `试听失败：${(e && e.message) || '无法播放（可能无版权或无可用音源）'}`,
            'error',
          )
          setPlayingKey('')
        },
      )
    },
    [playingKey, notify],
  )

  const handlePreviewToggle = async (checked) => {
    try {
      const r = await api.previewSetting(checked)
      setPreviewOn(Boolean(r && r.preview))
      notify(checked ? '已开启在线试听' : '已关闭在线试听', 'info')
    } catch (e) {
      notify(api.errMessage(e), 'error')
    }
  }

  const healthChip = useMemo(() => {
    if (!health) return <Chip size="small" label={translate('resources.musicsearch.health.unavailable')} />
    if (!health.enabled) {
      return <Chip size="small" label={translate('resources.musicsearch.health.disabled')} />
    }
    if (!health.reachable) {
      return (
        <Chip
          size="small"
          color="secondary"
          label={`${translate('resources.musicsearch.health.unavailable')}${
            health.detail ? `：${health.detail}` : ''
          }`}
        />
      )
    }
    return <Chip size="small" color="primary" label={translate('resources.musicsearch.health.ok')} />
  }, [health, translate])

  return (
    <div className={classes.root}>
      <div className={classes.header}>
        <Typography variant="h5">{translate('resources.musicsearch.title')}</Typography>
        {healthChip}
        <Tooltip title="开启后可在结果列表直接试听（会占用服务器出口带宽，生产环境建议保持关闭）">
          <FormControlLabel
            control={
              <Switch
                size="small"
                checked={previewOn}
                onChange={(e) => handlePreviewToggle(e.target.checked)}
                disabled={!health?.reachable}
              />
            }
            label="在线试听"
          />
        </Tooltip>
        <Button size="small" variant="outlined" onClick={() => setLoginOpen(true)}>
          {translate('resources.musicsearch.login.button')}
        </Button>
      </div>
      <Typography variant="body2" color="textSecondary">
        {translate('resources.musicsearch.subtitle')}
        {health?.dir ? `　入库目录：${health.dir}` : ''}
      </Typography>

      <Paper className={classes.bar} style={{ padding: 12 }}>
        <TextField
          className={classes.keyword}
          label={translate('resources.musicsearch.fields.keyword')}
          variant="outlined"
          size="small"
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') handleSearch()
          }}
        />
        <FormControl variant="outlined" size="small" style={{ minWidth: 130 }}>
          <InputLabel>{translate('resources.musicsearch.fields.type')}</InputLabel>
          <Select
            value={searchType}
            onChange={(e) => setSearchType(e.target.value)}
            label={translate('resources.musicsearch.fields.type')}
          >
            <MenuItem value="song">{translate('resources.musicsearch.fields.typeSong')}</MenuItem>
            <MenuItem value="album">{translate('resources.musicsearch.fields.typeAlbum')}</MenuItem>
            <MenuItem value="playlist">{translate('resources.musicsearch.fields.typePlaylist')}</MenuItem>
          </Select>
        </FormControl>
        <Button
          variant="contained"
          color="primary"
          onClick={handleSearch}
          disabled={searching || !health?.reachable}
        >
          {searching ? <CircularProgress size={18} /> : translate('resources.musicsearch.actions.search')}
        </Button>
      </Paper>

      <div className={classes.sources}>
        <Typography variant="body2" color="textSecondary">
          {translate('resources.musicsearch.fields.sources')}：
        </Typography>
        {SOURCE_OPTIONS.map((s) => (
          <FormControlLabel
            key={s.id}
            control={
              <Checkbox
                size="small"
                checked={sources.includes(s.id)}
                onChange={() => toggleSource(s.id)}
              />
            }
            label={s.name}
          />
        ))}
        <Typography variant="body2" color="textSecondary">
          ｜下载内容：
        </Typography>
        {TYPE_OPTIONS.map((t) => (
          <FormControlLabel
            key={t.id}
            control={
              <Checkbox
                size="small"
                checked={downloadTypes.includes(t.id)}
                onChange={() => toggleType(t.id)}
              />
            }
            label={t.label}
          />
        ))}
      </div>

      {running && (
        <div className={classes.progressBox}>
          <Typography variant="body2">
            {translate('resources.musicsearch.task.running')}　
            {translate('resources.musicsearch.task.progress', {
              done: task.processed || 0,
              total: task.total || 0,
            })}
            {task.skipped ? `　跳过 ${task.skipped}` : ''}
            {task.failed ? `　失败 ${task.failed}` : ''}
          </Typography>
          <LinearProgress
            variant={task.total > 0 ? 'determinate' : 'indeterminate'}
            value={task.total > 0 ? Math.round(((task.processed || 0) / task.total) * 100) : 0}
          />
          {task.current && (
            <Typography variant="caption" color="textSecondary">
              {translate('resources.musicsearch.task.current')}：{task.current}
            </Typography>
          )}
        </div>
      )}

      {!running && task && task.total > 0 && (
        <Typography variant="body2" style={{ marginTop: 12 }}>
          上次下载：完成 {task.done || 0}　跳过 {task.skipped || 0}　失败 {task.failed || 0}
          {task.errMsg ? `（${task.errMsg}）` : ''}
        </Typography>
      )}
      {!running && task && task.errors && task.errors.length > 0 && (
        <ul className={classes.errList}>
          {task.errors.slice(0, 8).map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}

      {results.length > 0 && (
        <>
          <TableContainer component={Paper} className={classes.table}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell padding="checkbox">
                    <Checkbox
                      size="small"
                      checked={allSelected}
                      onChange={toggleSelectAll}
                      disabled={!canDownload}
                    />
                  </TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.title')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.artist')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.album')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.duration')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.size')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.bitrate')}</TableCell>
                  <TableCell className={classes.head}>{translate('resources.musicsearch.table.source')}</TableCell>
                  {previewOn && <TableCell className={classes.head}>试听</TableCell>}
                </TableRow>
              </TableHead>
              <TableBody>
                {results.map((it) => (
                  <TableRow key={itemKey(it)} hover>
                    <TableCell padding="checkbox">
                      <Checkbox
                        size="small"
                        checked={Boolean(selected[itemKey(it)])}
                        onChange={() => toggleSelect(it)}
                        disabled={!canDownload}
                      />
                    </TableCell>
                    <TableCell>{it.name}</TableCell>
                    <TableCell>{it.artist}</TableCell>
                    <TableCell>{it.album}</TableCell>
                    <TableCell>{fmtDuration(it.duration)}</TableCell>
                    <TableCell>{fmtSize(it.size)}</TableCell>
                    <TableCell>{it.bitrate ? `${it.bitrate} kbps` : '-'}</TableCell>
                    <TableCell>{it.source}</TableCell>
                    {previewOn && (
                      <TableCell padding="none">
                        <Tooltip title={playingKey === itemKey(it) ? '停止' : '试听'}>
                          <IconButton size="small" onClick={() => togglePreview(it)}>
                            {playingKey === itemKey(it) ? (
                              <MdStop fontSize="small" />
                            ) : (
                              <MdPlayArrow fontSize="small" />
                            )}
                          </IconButton>
                        </Tooltip>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>

          <div className={classes.footer}>
            <Typography variant="body2">已选 {selectedCount} 首</Typography>
            <Button
              variant="contained"
              color="primary"
              onClick={handleDownload}
              disabled={!canDownload || running || selectedCount === 0}
            >
              {translate('resources.musicsearch.actions.download')}
            </Button>
            {running && (
              <Button variant="outlined" color="secondary" onClick={handleCancel}>
                {translate('resources.musicsearch.actions.cancel')}
              </Button>
            )}
            {!canDownload && (
              <Typography variant="caption" color="textSecondary">
                专辑/歌单结果暂不支持直接下载，请切换到「歌曲」类型
              </Typography>
            )}
          </div>
        </>
      )}

      {/* 隐藏的播放器：试听用 */}
      <audio ref={audioRef} style={{ display: 'none' }} onEnded={() => setPlayingKey('')} />

      <LoginDialog
        open={loginOpen}
        onClose={() => setLoginOpen(false)}
        onChanged={loadHealth}
      />
    </div>
  )
}

export default Search
