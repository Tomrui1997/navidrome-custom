window.global = window // fix "global is not defined" error in react-image-lightbox

import ReactDOM from 'react-dom'
import './index.css'
import { bootstrapLocale, applyHtmlLang } from './bootstrapLocale'
import { registerSW } from 'virtual:pwa-register'

registerSW({ immediate: true })

// 先把默认语言准备好，再动态加载 App：
// App 依赖链上的 i18nProvider 在模块加载时就确定了初始 locale，
// 因此必须在 import 之前完成翻译的读取，否则首屏会闪一下英文。
bootstrapLocale()
  .then((locale) => {
    applyHtmlLang(locale)
    return import('./App')
  })
  .then(({ default: App }) => {
    ReactDOM.render(<App />, document.getElementById('root'))
  })
  .catch((e) => {
    // 极端情况（如动态 import 失败）兜底：仍尝试渲染，避免白屏
    // eslint-disable-next-line no-console
    console.error('Failed to bootstrap application', e)
  })
