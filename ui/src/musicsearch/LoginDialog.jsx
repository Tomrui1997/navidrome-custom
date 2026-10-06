import React, { useCallback, useEffect, useRef, useState } from 'react'
import {
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Typography,
} from '@material-ui/core'
import { makeStyles } from '@material-ui/core/styles'
import { QRCodeSVG } from 'qrcode.react'
import { useNotify, useTranslate } from 'react-admin'
import * as api from './api'

// 支持扫码登录的平台（与后端白名单一致）
// accountName 用于「已登录」徽章：QQ 与微信扫码都会写入同一个 qq 凭据，故徽章统一显示该账号体系名
// linkOnly=true 的平台不渲染二维码，改用「链接登录」：酷狗 App 对网页二维码支持不佳，
// 且实测登录链接需要在手机浏览器打开才能完成确认。
const LOGIN_OPTIONS = [
  { id: 'qq', name: 'QQ音乐（QQ 扫码）', accountName: 'QQ音乐' },
  { id: 'qq_wx', name: 'QQ音乐（微信扫码）', accountName: 'QQ音乐' },
  { id: 'netease', name: '网易云音乐', accountName: '网易云音乐' },
  { id: 'kugou', name: '酷狗音乐（链接登录）', accountName: '酷狗音乐', linkOnly: true },
  { id: 'bilibili', name: 'Bilibili', accountName: 'Bilibili' },
]

const POLL_INTERVAL_MS = 2000
const MAX_POLL_COUNT = 150 // 约 5 分钟

const STATUS_TEXT = {
  waiting: '等待扫码…',
  scanned: '已扫码，请在手机上确认登录',
  success: '登录成功',
  expired: '二维码已过期，请重新获取',
  failed: '登录失败，请重试',
}

const useStyles = makeStyles((theme) => ({
  qrBox: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    gap: theme.spacing(1),
    marginTop: theme.spacing(2),
    minHeight: 200,
    justifyContent: 'center',
  },
  qrImg: {
    width: 200,
    height: 200,
    border: `1px solid ${theme.palette.divider}`,
    borderRadius: 4,
    imageRendering: 'pixelated',
  },
  // 链接型平台（酷狗 / 网易云 / B站）在本地渲染的二维码。
  // 注意：必须显式给白底 —— 深色主题下 SVG 背景透明会导致手机扫不出来。
  qrSvg: {
    width: 200,
    height: 200,
    borderRadius: 4,
    border: `1px solid ${theme.palette.divider}`,
    background: '#ffffff',
  },
  // 展示登录链接。
  // 颜色必须取自主题：原实现硬编码浅色背景且未设文字色，在深色主题下会继承白色文字 →
  // 形成「白底白字」完全看不见（真实缺陷）。
  linkBox: {
    width: '100%',
    wordBreak: 'break-all',
    background: theme.palette.background.default,
    color: theme.palette.text.primary,
    border: `1px solid ${theme.palette.divider}`,
    padding: theme.spacing(1),
    borderRadius: 4,
    fontSize: 12,
  },
  stateRow: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    flexWrap: 'wrap',
    marginTop: theme.spacing(1),
  },
}))

// LoginDialog 扫码登录对话框。
//
// 说明：
//   - QQ / QQ微信 上游直接返回二维码图片（data URI 或微信图片 URL），直接展示；
//   - 网易云 / Bilibili 上游只返回 h5 登录链接 → 用 qrcode.react **本地**渲染成二维码，
//     手机扫码即完成确认登录。**不引导用户在电脑上打开该链接**：实测电脑打开会被平台跳到
//     App 下载页（非登录页），且该链接本就是给手机用的。
//   - **酷狗**例外：改为「链接登录」—— 只展示链接 + 复制/打开按钮，让用户把链接发到
//     手机浏览器打开（酷狗 App 对网页二维码支持不佳）。
//   - 二维码在本地生成，不依赖任何外部服务（登录链接含一次性 token，不能外传给第三方）。
//   - 登录成功后上游会自动把 Cookie 写入服务端文件，前端拿不到也不需要拿到凭据。
const LoginDialog = ({ open, onClose, onChanged }) => {
  const classes = useStyles()
  const notify = useNotify()
  const translate = useTranslate()

  const [source, setSource] = useState('qq')
  const [session, setSession] = useState(null)
  const [status, setStatus] = useState('')
  const [message, setMessage] = useState('')
  const [loading, setLoading] = useState(false)
  const [loggedIn, setLoggedIn] = useState({})
  const pollCount = useRef(0)
  const timerRef = useRef(null)

  const stopPolling = useCallback(() => {
    if (timerRef.current) {
      clearInterval(timerRef.current)
      timerRef.current = null
    }
  }, [])

  const loadState = useCallback(async () => {
    try {
      const st = await api.loginState()
      setLoggedIn((st && st.loggedIn) || {})
    } catch (e) {
      // 登录态查询失败不阻塞页面
    }
  }, [])

  // 打开时重置并刷新登录态
  useEffect(() => {
    if (!open) {
      stopPolling()
      return undefined
    }
    setSession(null)
    setStatus('')
    setMessage('')
    loadState()
    return stopPolling
  }, [open, loadState, stopPolling])

  const handleCreate = useCallback(async () => {
    stopPolling()
    setLoading(true)
    setSession(null)
    setStatus('waiting')
    setMessage('')
    try {
      const res = await api.loginCreate(source)
      const data = (res && res.data) || {}
      if (!data.key) {
        throw new Error((res && res.msg) || '未获取到登录会话')
      }
      setSession(data)
      pollCount.current = 0
      timerRef.current = setInterval(async () => {
        pollCount.current += 1
        if (pollCount.current > MAX_POLL_COUNT) {
          stopPolling()
          setStatus('expired')
          return
        }
        try {
          const pr = await api.loginPoll({ source, key: data.key })
          const pd = (pr && pr.data) || {}
          const st = pd.status || ''
          setStatus(st)
          setMessage(pd.message || '')
          if (st === 'success') {
            stopPolling()
            notify(translate('resources.musicsearch.login.success'), 'info')
            await loadState()
            if (onChanged) onChanged()
          } else if (st === 'expired' || st === 'failed') {
            stopPolling()
          }
        } catch (e) {
          // 轮询偶发失败继续重试，直到超时
        }
      }, POLL_INTERVAL_MS)
    } catch (e) {
      setStatus('failed')
      setMessage(api.errMessage(e))
      notify(api.errMessage(e), 'error')
    } finally {
      setLoading(false)
    }
  }, [source, stopPolling, notify, translate, loadState, onChanged])

  const handleClose = () => {
    stopPolling()
    onClose()
  }

  const copiedRef = useRef(null)
  const handleCopy = async (text) => {
    try {
      await navigator.clipboard.writeText(text)
      notify(translate('resources.musicsearch.login.copied'), 'info')
    } catch (e) {
      copiedRef.current = text
      notify('复制失败，请手动选中链接复制', 'warning')
    }
  }

  // 同一账号体系去重（用 QQ 或微信扫码都写入 qq 凭据，徽章不应重复显示）
  const loggedNames = [
    ...new Set(LOGIN_OPTIONS.filter((o) => loggedIn[o.id]).map((o) => o.accountName)),
  ]

  // 当前平台是否为「链接登录」（酷狗）：不渲染二维码，只给链接与复制/打开按钮
  const currentOption = LOGIN_OPTIONS.find((o) => o.id === source)
  const linkOnly = Boolean(currentOption && currentOption.linkOnly)

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>{translate('resources.musicsearch.login.title')}</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="textSecondary">
          {translate('resources.musicsearch.login.hint')}
        </Typography>

        <div className={classes.stateRow}>
          <Chip
            size="small"
            color={loggedNames.length > 0 ? 'primary' : 'default'}
            label={
              loggedNames.length > 0
                ? `已登录：${loggedNames.join('、')}`
                : translate('resources.musicsearch.login.none')
            }
          />
          <Button size="small" onClick={loadState}>
            {translate('resources.musicsearch.login.refresh')}
          </Button>
        </div>

        <div className={classes.stateRow}>
          <FormControl variant="outlined" size="small" style={{ minWidth: 220 }}>
            <InputLabel>{translate('resources.musicsearch.login.platform')}</InputLabel>
            <Select
              value={source}
              onChange={(e) => setSource(e.target.value)}
              label={translate('resources.musicsearch.login.platform')}
            >
              {LOGIN_OPTIONS.map((o) => (
                <MenuItem key={o.id} value={o.id}>
                  {o.name}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <Button
            variant="contained"
            color="primary"
            onClick={handleCreate}
            disabled={loading}
          >
            {loading ? (
              <CircularProgress size={18} />
            ) : linkOnly ? (
              translate('resources.musicsearch.login.getLink')
            ) : (
              translate('resources.musicsearch.login.getQr')
            )}
          </Button>
        </div>

        <div className={classes.qrBox}>
          {session && session.image_url ? (
            <img className={classes.qrImg} src={session.image_url} alt="login qr" />
          ) : session && session.url ? (
            linkOnly ? (
              // 链接登录（酷狗）：不渲染二维码，避免手机扫码后交给 App 打不开的坑
              <>
                <Typography variant="body2" color="textSecondary">
                  {translate('resources.musicsearch.login.openInPhoneBrowser')}
                </Typography>
                <div className={classes.linkBox}>{session.url}</div>
                <div className={classes.stateRow}>
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={() => handleCopy(session.url)}
                  >
                    {translate('resources.musicsearch.login.copy')}
                  </Button>
                  <Button
                    size="small"
                    variant="contained"
                    color="primary"
                    onClick={() => window.open(session.url, '_blank', 'noopener')}
                  >
                    {translate('resources.musicsearch.login.open')}
                  </Button>
                </div>
              </>
            ) : (
              <>
                <QRCodeSVG
                  className={classes.qrSvg}
                  value={session.url}
                  size={200}
                  level="M"
                  includeMargin
                  bgColor="#ffffff"
                  fgColor="#000000"
                />
                <Typography variant="body2" color="textSecondary">
                  {translate('resources.musicsearch.login.openOnPhone')}
                </Typography>
                <div className={classes.linkBox}>{session.url}</div>
                <div className={classes.stateRow}>
                  <Button size="small" variant="outlined" onClick={() => handleCopy(session.url)}>
                    {translate('resources.musicsearch.login.copy')}
                  </Button>
                </div>
              </>
            )
          ) : (
            <Typography variant="body2" color="textSecondary">
              {translate('resources.musicsearch.login.placeholder')}
            </Typography>
          )}
          {status && (
            <Typography
              variant="body2"
              color={status === 'success' ? 'primary' : 'textSecondary'}
            >
              {STATUS_TEXT[status] || status}
              {message ? `（${message}）` : ''}
            </Typography>
          )}
        </div>
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose}>{translate('resources.musicsearch.login.close')}</Button>
      </DialogActions>
    </Dialog>
  )
}

export default LoginDialog
