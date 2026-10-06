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
import { MdFileDownload, MdPlayArrow, MdStop } from 'react-icons/md'
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
  // 结果列表可能很长（跨平台去重后常有 20~40 条），操作栏与**下载进度**都必须吸附在视口底部：
  // 进度原先渲染在表格**上方**，用户滚到列表里就看不见了（真实反馈：开始下载后看不到进度）。
  // 必须给不透明背景，否则滚动内容会透过操作栏。
  footer: {
    display: 'flex',
    flexDirection: 'column',
    gap: theme.spacing(0.5),
    marginTop: theme.spacing(2),
    position: 'sticky',
    bottom: 0,
    zIndex: 2,
    padding: theme.spacing(1, 2),
    background: theme.palette.background.paper,
    borderTop: `1px solid ${theme.palette.divider}`,
    borderBottom: `1px solid ${theme.palette.divider}`,
  },
  footerRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(2),
    flexWrap: 'wrap',
  },
  footerProgress: { flex: '1 1 320px', minWidth: 240 },
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

// 平台英文 id -> 中文名（通知文案用）
const sourceLabel = (id) =>
  (SOURCE_OPTIONS.find((s) => s.id === id) || {}).name || id

// 同名判定：标题忽略空白与大小写（同一首歌在各平台标题格式常有差异）
const normalizeTitle = (title) =>
  (title || '').replace(/\s+/g, '').toLowerCase()

// 试听自动降级时最多额外尝试的平台数：避免一次点击打出过多请求
const MAX_PREVIEW_FALLBACK = 3

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

  // 提交下载任务：底部「下载选中」与行内单曲下载共用，避免两处逻辑漂移。
  const submitDownload = async (items) => {
    if (items.length === 0) return
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

  const handleDownload = () => {
    const items = Object.values(selected)
    if (items.length === 0) {
      notify(translate('resources.musicsearch.errors.noSelection'), 'warning')
      return
    }
    submitDownload(items)
  }

  // 行内单曲下载：无需勾选、也无需滚到底，直接下载当前这一首（下载内容沿用上方勾选项）。
  const handleRowDownload = (it) => submitDownload([it])

  // 重试上一轮失败的条目（"未完成的下载"）：助手已保留原始条目，直接重新提交。
  const handleRetryFailed = () => {
    if (failedItems.length === 0) return
    submitDownload(failedItems)
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
  // 未完成数量：运行中 = 总任务数 - 已处理；结束后 = 失败数（失败即未完成）
  const remaining =
    running && task && task.total > 0 ? Math.max(0, task.total - (task.processed || 0)) : 0
  const lastTask = !running && task && task.total > 0 ? task : null
  // 助手快照保留了失败项**原始条目**，可直接作为新任务重试
  const failedItems = !running && task && Array.isArray(task.failedItems) ? task.failedItems : []

  // 同名异平台候选：搜索结果按平台各返回一份，某平台无音源时可自动降级到其它平台。
  const previewCandidates = useCallback(
    (it) => {
      const title = normalizeTitle(it.name)
      if (!title) return []
      return results
        .filter(
          (x) =>
            x.__kind === 'song' &&
            x.source !== it.source &&
            normalizeTitle(x.name) === title,
        )
        .slice(0, MAX_PREVIEW_FALLBACK)
    },
    [results],
  )

  // 试听：<audio> 无法携带 Authorization 头，改用 Navidrome 的 ?jwt= 查询参数认证（与 /api/events 同一机制）。
  // 播放前先做一次 1 字节预检，把后端返回的真实原因提示给用户；否则浏览器只会抛
  // 「no supported source was found」，无法区分平台风控、无音源、开关未开与登录过期。
  // 首条无音源时依次尝试同名异平台候选（QQ/酷狗部分歌曲受版权限制无直链，换平台往往能放）。
  const togglePreview = useCallback(
    async (it) => {
      const audio = audioRef.current
      if (!audio) return
      const key = itemKey(it)
      if (playingKey === key) {
        audio.pause()
        setPlayingKey('')
        return
      }
      let target = null
      let lastProbe = null
      for (const cand of [it, ...previewCandidates(it)]) {
        const candUrl = api.previewUrl(cand)
        let probe
        try {
          probe = await api.probePreview(candUrl)
        } catch (e) {
          // 预检请求本身失败（如断网）：降级为直接交给 <audio>，由播放失败分支兜底提示
          probe = { ok: true }
        }
        if (probe.ok) {
          target = { item: cand, url: candUrl }
          break
        }
        lastProbe = probe
      }
      if (!target) {
        notify(
          `试听失败：${api.previewErrorMessage(lastProbe || {})}`,
          'error',
        )
        setPlayingKey('')
        return
      }
      if (target.item.source !== it.source) {
        notify(
          `当前平台无可用音源，已自动切换 ${sourceLabel(
            target.item.source,
          )} 播放`,
          'info',
        )
      }
      audio.src = target.url
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
    [playingKey, previewCandidates, notify],
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
        {/* 前导全角空格写成 \u3000 转义：源码里直接写 U+3000 会触发 ESLint no-irregular-whitespace */}
        {health?.dir ? `\u3000入库目录：${health.dir}` : ''}
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

      {/* 下载进度与操作栏：统一放在底部吸底栏里（原先渲染在这里的进度块滚动即不可见） */}

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
                  {/* 行内下载按钮紧跟在「平台」之后：结果很多时无需滚到底部 */}
                  <TableCell className={classes.head} padding="none">
                    {translate('resources.musicsearch.table.download')}
                  </TableCell>
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
                    <TableCell padding="none">
                      <Tooltip
                        title={running ? '下载任务进行中，请稍候' : '下载这首（内容按上方勾选项）'}
                      >
                        {/* 用 span 包一层：disabled 的 IconButton 不触发事件，Tooltip 需要一个可 hover 的元素 */}
                        <span>
                          <IconButton
                            size="small"
                            // 图标按钮无可读文本，补 aria-label 便于无障碍阅读与单测定位
                            aria-label="下载这首"
                            disabled={!canDownload || running}
                            onClick={() => handleRowDownload(it)}
                          >
                            <MdFileDownload fontSize="small" />
                          </IconButton>
                        </span>
                      </Tooltip>
                    </TableCell>
                    {previewOn && (
                      <TableCell padding="none">
                        <Tooltip title={playingKey === itemKey(it) ? '停止' : '试听'}>
                          <IconButton
                            size="small"
                            aria-label={playingKey === itemKey(it) ? '停止' : '试听'}
                            onClick={() => togglePreview(it)}
                          >
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
            {running ? (
              <>
                {/* 运行中：进度、计数（含「剩余」）与当前文件全部放在吸底栏，滚到哪都看得到 */}
                <div className={classes.footerProgress}>
                  <Typography variant="body2">
                    {translate('resources.musicsearch.task.runningSummary', {
                      processed: task.processed || 0,
                      total: task.total || 0,
                      done: task.done || 0,
                      skipped: task.skipped || 0,
                      failed: task.failed || 0,
                      remaining,
                    })}
                    {task.elapsed ? `｜已用 ${task.elapsed}s` : ''}
                  </Typography>
                  <LinearProgress
                    variant={task.total > 0 ? 'determinate' : 'indeterminate'}
                    value={
                      task.total > 0 ? Math.round(((task.processed || 0) / task.total) * 100) : 0
                    }
                  />
                  {task.current && (
                    <Typography variant="caption" color="textSecondary" noWrap display="block">
                      {translate('resources.musicsearch.task.current')}：{task.current}
                    </Typography>
                  )}
                </div>
                <Button variant="outlined" color="secondary" onClick={handleCancel}>
                  {translate('resources.musicsearch.actions.cancel')}
                </Button>
              </>
            ) : (
              <>
                <div className={classes.footerRow}>
                  <Typography variant="body2">已选 {selectedCount} 首</Typography>
                  <Button
                    variant="contained"
                    color="primary"
                    onClick={handleDownload}
                    disabled={!canDownload || selectedCount === 0}
                  >
                    {translate('resources.musicsearch.actions.download')}
                  </Button>
                  {lastTask && (
                    <Typography variant="body2" color="textSecondary">
                      {translate('resources.musicsearch.task.lastRunSummary', {
                        done: lastTask.done || 0,
                        skipped: lastTask.skipped || 0,
                        failed: lastTask.failed || 0,
                      })}
                      {lastTask.errMsg ? `（${lastTask.errMsg}）` : ''}
                    </Typography>
                  )}
                  {failedItems.length > 0 && (
                    <Tooltip
                      title={
                        <span style={{ whiteSpace: 'pre-line' }}>
                          {(lastTask.errors || []).slice(0, 8).join('\n')}
                        </span>
                      }
                    >
                      {/* 失败即「未完成」：给出条数并提供一键重试 */}
                      <Button variant="outlined" color="primary" onClick={handleRetryFailed}>
                        {translate('resources.musicsearch.task.retryFailed', {
                          count: failedItems.length,
                        })}
                      </Button>
                    </Tooltip>
                  )}
                  {!canDownload && (
                    <Typography variant="caption" color="textSecondary">
                      专辑/歌单结果暂不支持直接下载，请切换到「歌曲」类型
                    </Typography>
                  )}
                </div>
              </>
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
