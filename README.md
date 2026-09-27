<a href="https://www.navidrome.org"><img src="resources/logo-192x192.png" alt="Navidrome logo" title="navidrome" align="right" height="60px" /></a>

# Navidrome Music Server &nbsp;[![Tweet](https://img.shields.io/twitter/url/http/shields.io.svg?style=social)](https://twitter.com/intent/tweet?text=Tired%20of%20paying%20for%20music%20subscriptions%2C%20and%20not%20finding%20what%20you%20really%20like%3F%20Roll%20your%20own%20streaming%20service%21&url=https://navidrome.org&via=navidrome)

[![Last Release](https://img.shields.io/github/v/release/navidrome/navidrome?logo=github&label=latest&style=flat-square)](https://github.com/navidrome/navidrome/releases)
[![Build](https://img.shields.io/github/actions/workflow/status/navidrome/navidrome/pipeline.yml?branch=master&logo=github&style=flat-square)](https://nightly.link/navidrome/navidrome/workflows/pipeline/master)
[![Downloads](https://img.shields.io/github/downloads/navidrome/navidrome/total?logo=github&style=flat-square)](https://github.com/navidrome/navidrome/releases/latest)
[![Docker Pulls](https://img.shields.io/docker/pulls/deluan/navidrome?logo=docker&label=pulls&style=flat-square)](https://hub.docker.com/r/deluan/navidrome)
[![Dev Chat](https://img.shields.io/discord/671335427726114836?logo=discord&label=discord&style=flat-square)](https://discord.gg/xh7j7yF)
[![Subreddit](https://img.shields.io/reddit/subreddit-subscribers/navidrome?logo=reddit&label=/r/navidrome&style=flat-square)](https://www.reddit.com/r/navidrome/)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-v2.0-ff69b4.svg?style=flat-square)](CODE_OF_CONDUCT.md)
[![Gurubase](https://img.shields.io/badge/Gurubase-Ask%20Navidrome%20Guru-006BFF?style=flat-square)](https://gurubase.io/g/navidrome)

Navidrome is an open source web-based music collection server and streamer. It gives you freedom to listen to your
music collection from any browser or mobile device. It's like your personal Spotify!


**Note**: The `master` branch may be in an unstable or even broken state during development. 
Please use [releases](https://github.com/navidrome/navidrome/releases) instead of 
the `master` branch in order to get a stable set of binaries.

---

# 📌 自建改造说明（本仓库非上游官方）

> 本仓库是基于上游 Navidrome **自建改造**的分支，新增了**百度网盘音乐同步**等能力。
> 改造**未合并到上游**，与上游官方项目无关；上游相关问题请提交至上游仓库，本仓库改动不代表上游立场。

| 声明项 | 内容 |
| --- | --- |
| 源项目 | [navidrome/navidrome](https://github.com/navidrome/navidrome) |
| 基线版本 | 上游 tag **`v0.64.2`** |
| 上游许可 | **GPL-3.0** |
| 本仓库许可 | **GPL-3.0**（衍生作品，沿用上游 `LICENSE`） |
| 接口项目 | [guohuiyuan/go-music-api](https://github.com/guohuiyuan/go-music-api)（**AGPL-3.0**，独立进程、**可选**） |
| 功能说明文档 | [`docs/使用手册.md`](docs/使用手册.md) |

## 一、源项目声明

本仓库为 [navidrome/navidrome](https://github.com/navidrome/navidrome) 的**衍生作品**，以该项目的
`v0.64.2` 版本为基线进行自建改造，**未做过任何上游合并**。

- 上游代码版权归 Navidrome 项目及其贡献者所有
- 上游以 **GPL-3.0** 授权（见仓库根目录 [`LICENSE`](LICENSE)）；依据 GPL-3.0，本仓库作为衍生作品
  **同样以 GPL-3.0 授权发布**，源码随二进制一并提供，任何再分发同样需遵守 GPL-3.0
- 本仓库新增/改造的代码位置（其余文件与上游 `v0.64.2` 保持一致）：

| 模块 | 路径 | 作用 |
| --- | --- | --- |
| 网盘同步客户端 | `core/baidusync/` | 与特权助手通信的 socket 客户端（Web 进程不接触凭据） |
| 特权助手 | `tools/nd-helper/` | root 助手：执行同步、下载、文件夹移动等特权动作 |
| 后端接口 | `server/nativeapi/` | 新增 `baidusync.go` / `mediaops.go` / `musicsearch.go` 等路由 |
| 前端（同步） | `ui/src/syncing/` | 「同步」页面与回收站 |
| 前端（搜索） | `ui/src/musicsearch/` | 「在线音乐搜索」页面 |
| 前端（删除） | `ui/src/common/DeleteSongsButton.jsx` | 歌曲列表批量删除入口 |

## 二、接口项目声明

本分支的「在线音乐搜索 / 下载入库 / 在线试听」能力**依赖一个独立部署的第三方接口服务**：

| 项 | 内容 |
| --- | --- |
| 接口项目 | [guohuiyuan/go-music-api](https://github.com/guohuiyuan/go-music-api) |
| 许可 | **AGPL-3.0** |
| 集成方式 | **独立进程部署**（默认仅监听 `127.0.0.1:18080`），Navidrome 通过 HTTP 调用其接口 |
| 是否复制源码 | **否**。本项目**未复制、未修改**该接口项目的代码，两者以进程边界隔离 |
| 是否必需 | **可选**。未部署时，「在线音乐搜索」页面会提示服务不可用，**其余功能完全不受影响** |
| 版权归属 | 该接口项目版权归其作者所有，本项目仅通过其公开 HTTP 接口调用，不对其做任何再分发 |

> ⚠️ **AGPL-3.0 合规提醒**：AGPL-3.0 要求「通过网络提供服务」时须公开源码。若你**修改**该接口项目
> 并对外提供服务，需按 AGPL-3.0 公开你修改后的源码。本项目自身与该项目**不存在衍生关系**。
> 该服务的部署与运维不在本仓库范围内，请遵循其项目自身的许可与文档。

## 三、百度网盘同步（本分支新增）

### 3.1 功能描述

把百度网盘上指定目录里的音乐**自动同步到 Navidrome 音乐库**，无需手动下载再上传：

- **网盘取文件**：调用服务器上的 `BaiduPCS-Go` 递归枚举网盘目录，按本轮计划文件清单逐个下载
- **只抓音乐相关文件**：歌曲（`.mp3` `.flac` `.m4a` `.aac` `.wav` `.ogg` `.opus` `.wma` `.ape`）、
  歌词（`.lrc`）、封面（`.jpg` `.jpeg` `.png` `.webp`）；其余类型（视频/文档等）一律跳过
- **保持目录结构**：按**远端父目录分组**下载，文件落到本地对应的子目录，不会全部堆在音乐目录外层
- **只增不减**：网盘删除的文件，本地**不会**自动删除
- **增量与覆盖**：「立即同步」自动跳过已存在文件；「全量校验（覆盖）」用于网盘替换过同名文件时强制重下
- **定时同步**：支持「每天固定时间」或「每 N 分钟」，由助手内置调度，改完即生效、无需重启服务
- **同步日志**：按天分文件，页面可按日期回看，超期自动清理（默认保留 7 天）
- **回收站**：删除走软删（本地 `trash/`、网盘 `/_deleted/`），可恢复

**设计上的两点关键取舍**（对应历史上出现过的真实问题）：

1. **同步前预检远端目录**（可列出且非空），结束后**扫描本轮日志判定成败** —— 杜绝
   「数据总量 0B + 退出码 0」的**假成功**
2. **进度只统计本次运行并按文件去重** —— 避免累加历史日志导致进度数字严重失真

### 3.2 前置条件

| 前置项 | 说明 |
| --- | --- |
| 网盘登录 | 需在**服务器上**由运维登录百度网盘账号（不支持扫码，仅账号密码 / BDUSS / Cookie） |
| 网盘目录 | 网盘上必须存在同步源目录（默认 `/apps/Cloud Sync/music`，即网盘网页端侧边栏「同步」目录） |
| 特权助手 | root 助手服务 `nd-helper` 已运行（`/run/nd-helper.sock`） |
| 权限 | 「同步」菜单**仅管理员**可见，接口层同样拒绝非管理员访问 |

> 🔒 **凭据安全**：网盘登录态保存在**服务器**上（root 用户的 `BaiduPCS-Go` 配置），
> 同步由 **root 特权助手**执行，**Web 进程不接触、不保存任何网盘凭据**，日志与接口也不回显凭据。

### 3.3 使用步骤

1. 以**管理员**身份登录，点击左侧导航 **「同步」**（地址 `/sync`）
2. 确认顶部状态区显示已登录的**网盘账号**（未登录时需先由运维在服务器完成登录）
3. 点击 **「立即同步」** 开始增量同步，可勾选「自动刷新」观察进度
4. 在**同步设置**中调整同步源/落地目录、包含歌词与封面、排除目录、线程数、定时同步等
5. 同步完成后 Navidrome **自动扫描**新文件入库，无需手动触发扫描
6. 底部**回收站**可恢复误删歌曲

### 3.4 同步范围（固定，页面无法绕过）

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| 网盘路径 | `/apps/Cloud Sync/music` | 同步源目录，**默认锁定** |
| 本地音乐目录 | `/opt/navidrome/music` | 落地目录，**默认锁定** |
| 锁定路径 | 开 | 防止页面误改导致同步范围跑偏，**推荐保持开启** |
| 排除目录 | `attachments` | 相对网盘路径，多个用英文逗号分隔；**不扫描、不下载** |
| 包含歌词 / 封面 | 均开 | 是否同步 `.lrc` 与封面图片 |
| 单文件线程数 / 并行文件数 | 16 / 4 | 按 SVIP 带宽设定；**非 SVIP 建议降到 4 / 2** |

> 详细逐项说明、常见问题与边界行为请见 [`docs/使用手册.md`](docs/使用手册.md) 第三章。

---

## [Check out our Live Demo!](https://www.navidrome.org/demo/)

__Any feedback is welcome!__ If you need/want a new feature, find a bug or think of any way to improve Navidrome, 
please file a [GitHub issue](https://github.com/navidrome/navidrome/issues) or join the discussion in our 
[Subreddit](https://www.reddit.com/r/navidrome/). If you want to contribute to the project in any other way 
([ui/backend dev](https://www.navidrome.org/docs/developers/), 
[translations](https://www.navidrome.org/docs/developers/translations/), 
[themes](https://www.navidrome.org/docs/developers/creating-themes)), please join the chat in our 
[Discord server](https://discord.gg/xh7j7yF). 

## Installation

See instructions on the [project's website](https://www.navidrome.org/docs/installation/)

## Cloud Hosting

[PikaPods](https://www.pikapods.com) has partnered with us to offer you an 
[officially supported, cloud-hosted solution](https://www.navidrome.org/docs/installation/managed/#pikapods). 
A share of the revenue helps fund the development of Navidrome at no additional cost for you.

[![PikaPods](https://www.pikapods.com/static/run-button.svg)](https://www.pikapods.com/pods?run=navidrome)

## Features
 
 - Handles very **large music collections**
 - Streams virtually **any audio format** available
 - Reads and uses all your beautifully curated **metadata**
 - Great support for **compilations** (Various Artists albums) and **box sets** (multi-disc albums)
 - **Multi-user**, each user has their own play counts, playlists, favourites, etc...
 - Very **low resource usage**
 - **Multi-platform**, runs on macOS, Linux and Windows. **Docker** images are also provided
 - Ready to use binaries for all major platforms, including **Raspberry Pi**
 - Automatically **monitors your library** for changes, importing new files and reloading new metadata 
 - Supports **lyrics** from sidecar .ttml, .yaml/.yml Lyricsfile, .elrc, .lrc, .srt, .txt files and embedded TTML, Enhanced LRC, LRC, SRT, and plain-text tags (via `lyricspriority`)
 - **Themeable**, modern and responsive **Web interface** based on [Material UI](https://material-ui.com)
 - **Compatible** with all Subsonic/Madsonic/Airsonic [clients](https://www.navidrome.org/docs/overview/#apps)
 - **Transcoding** on the fly. Can be set per user/player. **Opus encoding is supported**
 - Translated to **various languages**

## Translations

Navidrome uses [POEditor](https://poeditor.com/) for translations, and we are always looking 
for [more contributors](https://www.navidrome.org/docs/developers/translations/)

<a href="https://poeditor.com/"> 
<img height="32" src="https://github.com/user-attachments/assets/c19b1d2b-01e1-4682-a007-12356c42147c">
</a>

## Documentation
All documentation can be found in the project's website: https://www.navidrome.org/docs. 
Here are some useful direct links:

- [Overview](https://www.navidrome.org/docs/overview/)
- [Installation](https://www.navidrome.org/docs/installation/)
  - [Docker](https://www.navidrome.org/docs/installation/docker/)
  - [Binaries](https://www.navidrome.org/docs/installation/pre-built-binaries/)
  - [Build from source](https://www.navidrome.org/docs/installation/build-from-source/)
- [Development](https://www.navidrome.org/docs/developers/)
- [Subsonic API Compatibility](https://www.navidrome.org/docs/developers/subsonic-api/)

## Screenshots

<p align="left">
    <img height="550" src="https://raw.githubusercontent.com/navidrome/navidrome/master/.github/screenshots/ss-mobile-login.png">
    <img height="550" src="https://raw.githubusercontent.com/navidrome/navidrome/master/.github/screenshots/ss-mobile-player.png">
    <img height="550" src="https://raw.githubusercontent.com/navidrome/navidrome/master/.github/screenshots/ss-mobile-album-view.png">
    <img width="550" src="https://raw.githubusercontent.com/navidrome/navidrome/master/.github/screenshots/ss-desktop-player.png">
</p>
