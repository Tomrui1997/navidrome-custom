# Navidrome（自建分支）项目指令

> 每次执行任务**必须先阅读本文件**。开发流程标准见仓库内 `.github/skills/dev-process/SKILL.md`。
> 本仓库基于上游 `navidrome/navidrome` **v0.64.2** 自建，新增「百度网盘同步 + 歌曲删除」能力，其余遵循上游习惯。

---

## 一、项目概况

| 项 | 说明 |
| --- | --- |
| 源码目录 | `e:\Navidrome`（Go 后端 + React 前端 `ui/`） |
| 基线版本 | 上游 tag `v0.64.2`（提交 `10114574`）；本地为自建分支，改动未合并上游 |
| 后端 | Go（`go.mod` 要求 1.27，`GOTOOLCHAIN=auto` 自动拉取）；SQLite（**CGO 必需**） |
| 前端 | React + react-admin v3 + MUI v4（`ui/`，npm） |
| 自建模块 | `tools/nd-helper/`（root 特权助手）、`core/baidusync/`（socket 客户端）、`server/nativeapi/{baidusync,mediaops}.go`、`ui/src/syncing/`、`ui/src/common/DeleteSongsButton.jsx` |
| 测试环境 | `192.168.171.128`（CentOS 8，glibc 2.28，二进制 + systemd） |
| 生产环境 | `124.222.150.220`（CentOS Stream 9，nginx 子路径 `/music`）；**对外访问一律用域名 `https://lxssc.com`，禁止用 IP 访问**（nginx 已配置 IP 直连拦截） |

## 二、固定要求

1. 不要主动输出代码块，使用编辑工具直接修改文件
2. 回复使用中文，语言简洁
3. 新增或修改的代码必须添加**中文注释**；复杂逻辑注释「为什么」
4. 修复缺陷时必须排查其他模块是否存在同类问题，一并修复
5. **修改必须验证通过才允许声称完成**（编译 + 静态检查 + 实测，缺一不可）
6. **交付纪律**：功能改完 → 先部署**测试环境**验证 → **等用户通知** → 才部署生产
7. 敏感信息（百度 Cookie/BDUSS、口令）**禁止**出现在命令行、对话、日志中；凭据只走文件 + 用后 shred
8. 不提交编译产物与临时脚本；提交前更新 `CHANGELOG.md`

## 三、验证与构建（改动后必须执行）

```powershell
# 1) 助手（含单测，本机 Windows 可跑）
cd e:\Navidrome
go vet ./tools/nd-helper/... ; go test ./tools/nd-helper/... ; gofmt -l tools\nd-helper
# 注意：跨平台编译后再跑测试前要清掉 GOOS/GOARCH：Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED

# 2) 一键构建 + 部署（内部：前端构建 → zig 交叉编译 → 助手构建 → scp → 远端校验哈希 → 安装 → 重启 → 探活）
powershell -ExecutionPolicy Bypass -File e:\navidrome-deploy\90-build-deploy.ps1 -Target test
```

- 交叉编译参数：`GOOS=linux GOARCH=amd64 CGO_ENABLED=1`、`CC="E:\tools\zig-x86_64-windows-0.15.2\zig.exe cc -target x86_64-linux-gnu.2.28"`、tags `netgo,sqlite_fts5`
- 远端脚本 `e:\navidrome-deploy\deploy-remote.sh` **幂等**（校验哈希→备份一次→安装→重启→状态），中断后可安全重跑
- 生产：`… -Target prod -SkipBuild`（复用已构建产物），**仅在用户通知后执行**

## 四、本机环境坑（务必遵守，避免反复踩）

1. **PowerShell 5.1 会把原生命令的 stderr 当终止性错误** → 用 `cmd /c "… > log 2>&1"` 包裹再取 `$LASTEXITCODE`
2. **远端命令不能带 `$`**（会被本机 PS 抢先解释）→ 复杂逻辑写成 `.sh` 脚本、LF 归一化后 `scp` 过去 `bash` 执行
3. **不要在终端工具里用 here-string（`@'…'@`）拼长命令** → 用文件写入工具落盘脚本
4. 项目源码行尾：`.go` 文件用 **LF**（`conf/configuration.go` 等少数为 CRLF，勿整文件改写）
5. `gofmt -w` 后再检查 `gofmt -l`；结构体字段对齐易被注释块打断，必须跑一次 `gofmt -w`
6. 浏览器验证：`page.goto` 到仅 hash 不同的同一 URL 属同文档导航不重拉 HTML → 需 `Network.setCacheDisabled` + `page.reload()`

## 五、开发流程标准（全局强制）

> 以 `.github/skills/dev-process/SKILL.md` 为准，**禁止跳阶段**。

| 阶段 | 出口门禁 |
| --- | --- |
| 一、需求接收与分析 | 需求确认无歧义、MoSCoW 优先级已排序、无阻断性技术风险 |
| 二、需求优化与确认 | PRD（含 Given-When-Then 验收标准）评审通过、基线锁定 |
| 三、编码开发 | 技术方案评审通过、编码规范达标、开发自测通过、CR 通过 |
| 四、代码测试 | P0 清零、P1 修复率 ≥95%、核心流程 100%、无高危漏洞 |
| 五、发布上线 | 上线前 Checklist 完整、回滚预案可执行、发布后验证通过 |

### 本仓库每次改代码的最低要求

1. **先方案后编码**：新模块/复杂功能先出技术方案（`docs/` 或 `e:\navidrome-deploy\design\`），评审通过再写码
2. **命名与结构**：函数 ≤ 50 行、嵌套 ≤ 3 层；`handler → helper/client` 单向调用
3. **安全编码**：外部输入必校验；路径必须做白名单/包含性校验（`ensureWithin`）；特权操作走 root 助手，Web 进程不接触凭据
4. **日志**：用 `log.Printf` 带前缀；**日志按天分文件**（`/var/log/baidusync/YYYY-MM-DD.log`），过滤无意义噪音，保留 7 天
5. **开发自测清单**：与验收标准逐条对齐、覆盖边界（空/超长/不存在/无权限）、无调试残留、无硬编码敏感信息
6. **影响面检查**：改动前评估关联模块（前端菜单/路由/i18n、后端路由鉴权、助手动作白名单）
7. **提交规范**：先更新 `CHANGELOG.md`（日期 + 编号），提交信息中文，禁止提交二进制与临时脚本

### 缺陷分级与响应

| 等级 | 定义 | 响应 | 修复 |
| --- | --- | --- | --- |
| P0 | 主流程阻断 / 数据丢失 / 误删文件 | 立即 | 24 小时内 |
| P1 | 核心功能不可用 / 同步结果错误 | 2 小时内 | 48 小时内 |
| P2 | 非核心功能异常 / 体验问题 | 当天 | 本迭代内 |
| P3 | 文案 / 优化建议 | 当天 | 按需排期 |

## 六、本次自建功能要点（避免回退）

- 同步范围**钉死**：`remoteRoot=/apps/Cloud Sync/music`、`localRoot=/opt/navidrome/music`（`lockPaths=true` 时接口拒绝改路径）
- 只抓歌曲/歌词/封面（扩展名白名单）+ 排除目录（默认 `attachments`，可配）
- 下载**按远端父目录分组**，`--saveto` 指向对应本地子目录（否则文件会落到 music 外层）
- 同步前**预检远端目录**（`baidupcs ls` 可列出且非空），结束后**扫描本轮日志**判成败：杜绝「数据总量 0B + 退出码 0」的假成功
- 进度只统计**本次运行**并按文件去重（历史实现累加整份日志，数字严重失真）
- 删除走**软删**（本地 `trash/`、网盘 `/_deleted/`，可恢复）；配置开启软删时硬删请求会被强制改回
- 测试机无 IPv6 出口：需 `/etc/hosts` 固定百度域名为 IPv4 + 内核禁用 IPv6（见 `e:\navidrome-deploy\97-pin-ipv4.sh`）
