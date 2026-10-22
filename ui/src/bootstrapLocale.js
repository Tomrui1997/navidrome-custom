import config from './config'
import { REST_URL } from './consts'
import { baseUrl } from './utils/urls'

const TRANSLATION_KEY = 'translation'
const LOCALE_KEY = 'locale'

// 读取本地缓存的翻译记录，缓存结构为 { id, name, data, termCount }
const readCachedTranslation = () => {
  try {
    return JSON.parse(localStorage.getItem(TRANSLATION_KEY))
  } catch (e) {
    return null
  }
}

/**
 * 首屏语言自举：在渲染 App 之前把目标语言确定下来。
 *
 * 背景：i18nProvider 的初始 locale 在模块加载时就算好了，
 * 没有可用的缓存翻译时只能回退 en，于是首次访问（或清了缓存 / 无痕窗口）
 * 第一屏永远是英文，要等 App 的 effect 异步取回翻译才切成中文。
 * 更糟的是 App 的 effect 只在「没有 locale」时才补救，
 * 一旦出现「有 locale 但翻译缓存丢了」就会永远卡在英文。
 * 这里改成先取好翻译再动态加载 App，保证首屏就是目标语言。
 *
 * @returns {Promise<string>} 最终生效的 locale
 */
export async function bootstrapLocale() {
  const defaultLanguage = config.defaultLanguage || ''
  const savedLocale = localStorage.getItem(LOCALE_KEY)
  // 用户曾手动选过语言就尊重它，否则用服务端默认语言
  const target = savedLocale || defaultLanguage

  if (!target) {
    return 'en'
  }

  const cached = readCachedTranslation()
  // 已有匹配的缓存翻译，i18nProvider 会直接用它，无需等待网络
  if (cached && cached.id === target) {
    return target
  }

  // 英文是内置的，不需要请求
  if (target === 'en') {
    localStorage.setItem(LOCALE_KEY, target)
    return target
  }

  try {
    const res = await fetch(
      baseUrl(`${REST_URL}/translation/${encodeURIComponent(target)}`),
      {
        headers: { Accept: 'application/json' },
        // 翻译接口是公开资源，带上凭据可避免反代场景下丢失身份
        credentials: 'same-origin',
      },
    )
    if (!res.ok) {
      throw new Error(`HTTP ${res.status}`)
    }
    const record = await res.json()
    localStorage.setItem(TRANSLATION_KEY, JSON.stringify(record))
    localStorage.setItem(LOCALE_KEY, target)
    return target
  } catch (e) {
    // 取不到翻译不能阻塞渲染，退回英文（与 i18nProvider 的兜底一致）
    // eslint-disable-next-line no-console
    console.error(`Cannot load language "${target}": ${e}`)
    return 'en'
  }
}

/**
 * 设置 <html lang>，供读屏软件与浏览器翻译判断页面语言。
 * 值需与 SSR 模板注入的 HtmlLang 保持一致，避免首屏抖动。
 */
export function applyHtmlLang(locale) {
  document.documentElement.lang = locale || 'en'
}
