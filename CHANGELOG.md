# 变更日志（自建分支）

> 本项目基于上游 `navidrome/navidrome` v0.64.2 自建，此处只记录**自建改动**；上游变更见 GitHub 发布说明。
> 格式：日期 + 编号 + 说明（含验证方式）。

## 2026年9月27日 更新

### 1. 新增「百度网盘同步」功能（管理后台 → 同步）

- 侧边栏新增「同步」入口（`/sync`，仅管理员）：状态区（网盘账号/任务状态/上次同步/下次计划）、操作按钮、配置表单、同步日志、回收站
- 配置项全部可视化可保存：网盘路径、本地目录、歌词/封面开关、单文件线程数、并行文件数、回收站保留天数、网盘回收目录、定时开关/模式/时间、**排除目录**、**锁定路径**、**日志保留天数**
- 支持**手动同步**与**定时同步**（daily 固定时间 / interval 每 N 分钟），均为**增量**（已存在文件跳过）
- 同步范围**钉死**在音乐目录：路径可锁定（`lockPaths`）、排除目录（默认 `attachments`）、只抓歌曲/歌词/封面扩展名白名单
- 同步前预检远端目录、结束后扫描本轮日志判定成败，杜绝「数据总量 0B + 退出码 0」的假成功
- 进度只统计**本次运行**并按文件去重（分母=本轮计划数）

### 2. 新增「歌曲删除」功能（歌曲列表批量操作栏）

- 支持单选/多选删除，删除范围三选项：仅库记录 / 删本地 / 删网盘（默认勾选前两项）
- 打开对话框即**干跑预演**，显示每首的本地/网盘动作与网盘目标路径；勾选范围变化自动重新预演
- **软删除**优先：本地进 `trash/files/…`、网盘 mv 到 `/_deleted/…`，回收站可恢复/彻底删除，超期自动清理
- 配置开启软删时，接口传硬删会被强制改回软删（安全兜底）
- 所有破坏性操作写审计日志（`/var/log/nd-helper-audit.log`）

### 3. 新增 root 特权助手 `nd-helper`（Unix socket）

- 百度凭据只保留在 root 的 `/opt/navidrome/baidusync.json`(0640)，Web 进程全程不接触
- 动作白名单：`status/config_get/config_set/login_status/sync_start/sync_cancel/log_tail/delete/trash_*`
- systemd 单元限制：`MemoryMax=256M`、`CPUQuota=50%`、`ProtectSystem=full`；取消任务按**进程组**终止

### 4. 默认语言改为简体中文

- `conf/configuration.go` 默认 `defaultlanguage=zh-Hans`；补齐 `resources/i18n/zh-Hans.json` 中同步/删除相关文案

### 5. 同步日志按天分文件（解决日志过大打不开）

- 日志目录 `/var/log/baidusync/YYYY-MM-DD.log`；页面支持按日期回看
- 过滤 baidupcs 逐文件元数据噪音、压缩超长直链；尾部读取改为**倒读固定块**（不再整文件扫描）
- 保留天数可配（默认 7 天），启动与每日调度自动清理

### 6. 缺陷修复（本轮联调发现，全部复测通过）

| 编号 | 级别 | 问题 | 修复 |
| --- | --- | --- | --- |
| 1 | P0 | 同步假成功（显示完成但没下文件） | 远端目录预检 + 本轮日志失败模式扫描 + 枚举为 0 报错 |
| 2 | P0 | 263 个文件被写到 `music/` 外层 | 按远端父目录分组下载，`--saveto` 指向对应本地子目录 |
| 3 | P1 | 进度数字失真（累计全量日志行数） | 只统计本轮日志区间 + 路径去重 + 计划数作分母 |
| 4 | P1 | 远端枚举恒为 0（树形输出解析错误） | 按缩进用父目录栈重建完整路径；补单测 |
| 5 | P1 | 下载大量重试/失败（IPv6 不可达） | `/etc/hosts` 固定百度域名为 IPv4 + 内核禁用 IPv6；网络类错误仅在「本轮 0 产出」时判失败 |
| 6 | P2 | 删除对话框崩溃（React #130） | 对话框按钮改用 MUI Button（react-admin Button 需图标子节点） |
| 7 | P2 | 删除按钮只显示图标 | 批量栏改用 react-admin Button（label + 图标）并加 `aria-label` |
| 8 | P2 | 手动同步后调度器立即补跑 | 同步成功写入 `LastRunDay/LastRunTime` |
| 9 | P3 | 回收站 `size` 恒为 0 | 移动文件前先取大小 |
| 10 | P3 | 日志噪音 `[ undefined]` | 统一清洗日志行 |

### 7. 规范与文档

- 落盘项目规范 `.github/copilot-instructions.md` 与 `.github/skills/dev-process/SKILL.md`
- 新增 `docs/BaiduSync-PRD.md`（含 Given-When-Then 验收标准与 MoSCoW 优先级）
- 新增 `docs/BaiduSync-测试报告.md`（用例表、13 个缺陷与修复、未覆盖项）
- 新增 `docs/CR-checklist.md`（提交前自查清单）
- 新增助手单测 8 个用例（树解析/排除目录/扩展名过滤/ls 计数/日志过滤/按天路径/倒读尾部/完成数去重）

### 验证方式

- `go vet ./tools/nd-helper/...` 0 问题、`gofmt -l` 无输出、`go test ./tools/nd-helper/...` 8/8 通过
- 一键构建部署：`powershell -File e:\navidrome-deploy\90-build-deploy.ps1 -Target test`（含 SHA256 校验与回滚命令）
- 测试环境实测：同步 1.209GB/4m32s（≈4.4MB/s）、增量跳过、范围排除、路径锁定、删除→回收站→恢复、权限校验
- 生产部署：**等待用户通知后执行**
