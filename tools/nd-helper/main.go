// Package main 实现 Navidrome 的「百度网盘同步/删除」特权助手（nd-helper）。
//
// 设计目标（详见 docs：BaiduSync-tech-design.md）：
//   - 百度网盘凭据只保留在 root 配置目录，Web 服务（以 navidrome 用户运行）全程不接触凭据；
//   - 助手仅监听本地 Unix socket，且只暴露白名单动作；
//   - 所有破坏性操作（删除/恢复/清理/改配置）写审计日志，且路径必须落在配置的根目录之内。
//
// 启动方式：由 systemd 以 root 身份运行（nd-helper.service）。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSocketPath = "/run/nd-helper.sock"
	defaultConfigPath = "/opt/navidrome/baidusync.json"
	defaultStatePath  = "/opt/navidrome/.baidusync-state.json"
	defaultAuditLog   = "/var/log/nd-helper-audit.log"
	maxRequestBytes   = 1 << 20 // 1MB，防超大请求
)

// Schedule 定时同步配置。支持「每天固定时刻」与「固定间隔」两种模式，
// 由助手内置调度器执行（页面改配置后无需重载 systemd）。
type Schedule struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"` // daily | interval
	At      string `json:"at"`   // HH:MM，mode=daily 时使用
	Minutes int    `json:"minutes"`
}

// Config 同步与删除的全部可配置项，落盘为 JSON（属主 root:navidrome，0640）。
type Config struct {
	Enabled       bool   `json:"enabled"`
	BaidupcsBin   string `json:"baidupcsBin"`
	RemoteRoot    string `json:"remoteRoot"`
	LocalRoot     string `json:"localRoot"`
	IncludeLyrics bool   `json:"includeLyrics"`
	IncludeCovers bool   `json:"includeCovers"`
	// ExcludeDirs 远端根目录下要跳过的目录（相对路径前缀），例如 attachments。
	// 同步只抓歌曲/歌词/封面，这些目录里的东西一律不扫不下载。
	ExcludeDirs []string `json:"excludeDirs"`
	// LockPaths 锁定 remoteRoot/localRoot，防止页面误改导致同步范围跑偏。
	LockPaths      bool     `json:"lockPaths"`
	Threads        int      `json:"threads"`
	ParallelFiles  int      `json:"parallelFiles"`
	DeleteExtra    bool     `json:"deleteExtra"` // 是否清理网盘已删除的本地文件（默认 false，保持只增不减）
	Schedule       Schedule `json:"schedule"`
	SoftDelete     bool     `json:"softDelete"`
	TrashDir       string   `json:"trashDir"`
	RemoteTrashDir string   `json:"remoteTrashDir"`
	RetentionDays  int      `json:"retentionDays"`
	// LogRetentionDays 同步日志（按天分文件）保留天数，默认 7。
	LogRetentionDays int `json:"logRetentionDays"`
	// LimitFiles 单次同步最多处理的文件数（0 或不填 = 不限）。
	// 用途：验证阶段只想跑十几首验证流程，不必整库扫一遍（整库要等几分钟且白跑流量）。
	LimitFiles int `json:"limitFiles"`

	// ------ 在线搜索（go-music-api）与下载入库 ------ //

	// MusicApiEnabled 在线搜索总开关。关闭后助手直接拒绝下载请求。
	MusicApiEnabled bool `json:"musicApiEnabled"`
	// MusicApiBaseURL go-music-api 服务地址。**必须是回环地址**（安全约束，见 validateConfig），
	// 防止把「取直链/下载」指向外部主机造成 SSRF。
	MusicApiBaseURL string `json:"musicApiBaseURL"`
	// MusicApiSources 默认搜索平台（英文逗号分隔）。
	MusicApiSources string `json:"musicApiSources"`
	// MusicApiTimeoutSec 单次接口调用超时（秒）。
	MusicApiTimeoutSec int `json:"musicApiTimeoutSec"`
	// EnablePreview 是否允许在线试听（会占用服务器出口带宽，默认关闭）。
	EnablePreview bool `json:"enablePreview"`
	// DownloadDir 下载入库目录。单独子目录，避免与网盘同步的内容互相覆盖。
	DownloadDir string `json:"downloadDir"`
	// DownloadConcurrency 并发下载数（保护生产出口带宽）。
	DownloadConcurrency int `json:"downloadConcurrency"`
	// DownloadOverwrite 同名文件是否覆盖（默认 false = 跳过，保证增量语义）。
	DownloadOverwrite bool `json:"downloadOverwrite"`
	// DownloadTypes 下载内容类型：audio / lyric / cover。
	DownloadTypes []string `json:"downloadTypes"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		BaidupcsBin:   "/usr/local/bin/baidupcs",
		RemoteRoot:    "/apps/Cloud Sync/music",
		LocalRoot:     "/opt/navidrome/music",
		IncludeLyrics: true,
		IncludeCovers: true,
		// 默认排除非音乐目录（老网盘同步残留的 attachments/ 里全是无关图片）
		ExcludeDirs: []string{"attachments"},
		LockPaths:   true,
		// 默认定值按 SVIP 带宽/连接数给得高一些（单文件 16 线程 × 4 并发文件 = 64 连接）；
		// 非 SVIP 账号建议在页面上降到 4/2，避免被限速。
		Threads:        16,
		ParallelFiles:  4,
		DeleteExtra:    false,
		Schedule:       Schedule{Enabled: true, Mode: "daily", At: "03:00", Minutes: 360},
		SoftDelete:     true,
		TrashDir:       "/opt/navidrome/trash",
		RemoteTrashDir: "/_deleted",
		RetentionDays:  30,
		// 日志按天分文件，默认保留 7 天（避免单文件过大导致页面打开卡死）
		LogRetentionDays: 7,
		// 默认不限制文件数（正式运行必须全量增量对比）；测试时可在页面改成 10 左右
		LimitFiles: 0,

		// 在线搜索默认值：地址固定回环（仅本机可访问）；默认不开启在线试听（省带宽）。
		MusicApiEnabled:     true,
		MusicApiBaseURL:     "http://127.0.0.1:18080",
		MusicApiSources:     "netease,kugou,kuwo,migu,qq",
		MusicApiTimeoutSec:  15,
		EnablePreview:       false,
		DownloadDir:         "/opt/navidrome/music/在线音乐",
		DownloadConcurrency: 2,
		DownloadOverwrite:   false,
		DownloadTypes:       []string{"audio", "lyric", "cover"},
	}
}

// State 记录调度与同步任务的持久化状态（重启不丢）。
type State struct {
	LastSyncStart time.Time `json:"lastSyncStart"`
	LastSyncEnd   time.Time `json:"lastSyncEnd"`
	LastSyncOK    bool      `json:"lastSyncOk"`
	LastSyncMsg   string    `json:"lastSyncMsg"`
	LastRunDay    string    `json:"lastRunDay"`  // daily 模式：已执行的日期（YYYY-MM-DD）
	LastRunTime   time.Time `json:"lastRunTime"` // interval 模式：上次执行时间
}

// Request / Response：Unix socket 上的一问一答协议。
type Request struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// Server 持有全局状态。
type Server struct {
	socketPath string
	configPath string
	statePath  string
	auditLog   string

	mu       sync.Mutex // 保护 config/state/trash 索引的读写
	cfg      Config
	state    State
	syncTask *SyncTask
	// downloadTask 在线下载任务（与同步任务相互独立，可并行）
	downloadTask *DownloadTask
}

func main() {
	socket := flag.String("socket", defaultSocketPath, "Unix socket 路径")
	cfgPath := flag.String("config", defaultConfigPath, "配置文件路径")
	statePath := flag.String("state", defaultStatePath, "状态文件路径")
	auditPath := flag.String("audit", defaultAuditLog, "审计日志路径")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[nd-helper] ")

	s := &Server{socketPath: *socket, configPath: *cfgPath, statePath: *statePath, auditLog: *auditPath}

	// 读配置（缺失则写默认值）与状态
	if err := s.loadConfig(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := s.loadState(); err != nil {
		log.Printf("加载状态失败（忽略，使用默认）: %v", err)
	}
	// 上次同步「已开始但未结束」说明进程在同步途中被杀（重启/崩溃/被强杀）。
	// 必须显式标注中断：否则前端只显示上一次成功时间，看起来像任务还在跑或无声失败，
	// 用户无法判断到底该等待还是该重跑。
	s.markInterruptedSync()
	// 清理超期回收站内容（后台，不阻塞启动）
	go s.cleanupTrashLoop()
	// 日志按天分文件：启动时先清一次过期日志
	go func() {
		s.mu.Lock()
		keep := s.cfg.LogRetentionDays
		s.mu.Unlock()
		cleanupOldLogs(keep)
	}()
	// 启动内置调度器
	go s.scheduleLoop()

	if err := s.serve(); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 配置与状态读写
// ---------------------------------------------------------------------------

func (s *Server) loadConfig() error {
	b, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = DefaultConfig()
		if werr := s.saveConfigLocked(); werr != nil {
			return fmt.Errorf("写入默认配置失败: %w", werr)
		}
		log.Printf("未找到配置，已写入默认配置: %s", s.configPath)
		return nil
	}
	if err != nil {
		return err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("解析配置失败: %w", err)
	}
	s.cfg = cfg
	log.Printf("已加载配置: remoteRoot=%q localRoot=%q 定时=%v(%s)",
		cfg.RemoteRoot, cfg.LocalRoot, cfg.Schedule.Enabled, cfg.Schedule.Mode)
	return nil
}

// saveConfigLocked 原子写入配置文件，并保持 0640 与 root:navidrome 属主。
func (s *Server) saveConfigLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(s.configPath, append(b, '\n'), 0o640); err != nil {
		return err
	}
	chownNavidrome(s.configPath)
	return nil
}

func (s *Server) loadState() error {
	b, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.state)
}

func (s *Server) saveStateLocked() {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return
	}
	_ = atomicWrite(s.statePath, append(b, '\n'), 0o600)
}

// markInterruptedSync 检测并标记「上次同步未正常结束」（进程被杀导致的静默中断）。
func (s *Server) markInterruptedSync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LastSyncStart.IsZero() || !s.state.LastSyncEnd.Before(s.state.LastSyncStart) {
		return
	}
	s.state.LastSyncEnd = s.state.LastSyncStart
	s.state.LastSyncOK = false
	s.state.LastSyncMsg = "上次同步被中断（服务重启），请重新执行同步"
	s.saveStateLocked()
	log.Printf("检测到上次同步被中断（开始于 %s），已标记状态", s.state.LastSyncStart.Format("2006-01-02 15:04:05"))
}

// atomicWrite 先写临时文件再 rename，避免半截文件。
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// chownNavidrome 尽力把文件属主改为 root:navidrome（失败仅记录，不致命）。
func chownNavidrome(path string) {
	grp, err := user.LookupGroup("navidrome")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return
	}
	_ = os.Chown(path, 0, gid)
}

// audit 写审计日志（破坏性操作必须调用）。
func (s *Server) audit(format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	f, err := os.OpenFile(s.auditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Printf("写审计日志失败: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// ---------------------------------------------------------------------------
// Unix socket 服务与动作分发
// ---------------------------------------------------------------------------

func (s *Server) serve() error {
	// 清理上次残留的 socket 文件
	if _, err := os.Stat(s.socketPath); err == nil {
		_ = os.Remove(s.socketPath)
	}
	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	// 仅属主 root 与同组 navidrome 可访问
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		log.Printf("设置 socket 权限失败: %v", err)
	}
	chownNavidrome(s.socketPath)
	log.Printf("监听中: %s", s.socketPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("accept 失败: %v", err)
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	raw, err := bufio.NewReader(io.LimitReader(conn, maxRequestBytes)).ReadBytes('\n')
	if err != nil && len(raw) == 0 {
		writeResp(conn, Response{OK: false, Error: "读取请求失败: " + err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		writeResp(conn, Response{OK: false, Error: "解析请求失败: " + err.Error()})
		return
	}
	data, aerr := s.dispatch(req)
	if aerr != nil {
		log.Printf("动作 %s 失败: %v", req.Action, aerr)
		writeResp(conn, Response{OK: false, Error: aerr.Error()})
		return
	}
	writeResp(conn, Response{OK: true, Data: data})
}

func writeResp(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

// dispatch 白名单动作分发。
func (s *Server) dispatch(req Request) (any, error) {
	switch req.Action {
	case "status":
		return s.actionStatus()
	case "config_get":
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cfg, nil
	case "config_set":
		var cfg Config
		if err := json.Unmarshal(req.Params, &cfg); err != nil {
			return nil, fmt.Errorf("配置参数解析失败: %w", err)
		}
		return s.actionConfigSet(cfg)
	case "login_status":
		return s.actionLoginStatus()
	case "sync_start":
		var p struct {
			Mode  string   `json:"mode"`
			Types []string `json:"types"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return s.actionSyncStart(p.Mode, p.Types, "manual")
	case "sync_cancel":
		return s.actionSyncCancel()
	case "log_tail":
		var p struct {
			Lines int    `json:"lines"`
			Date  string `json:"date"` // 可选：YYYY-MM-DD，缺省为今天
		}
		_ = json.Unmarshal(req.Params, &p)
		return s.actionLogTail(p.Lines, p.Date)
	case "delete":
		var p DeleteParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("删除参数解析失败: %w", err)
		}
		return s.actionDelete(p)
	case "trash_list":
		return s.actionTrashList()
	case "trash_restore":
		var p struct {
			IDs []string `json:"ids"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return s.actionTrashRestore(p.IDs)
	case "trash_purge":
		var p struct {
			IDs          []string `json:"ids"`
			DeleteRemote bool     `json:"deleteRemote"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return s.actionTrashPurge(p.IDs, p.DeleteRemote)
	case "download_start":
		var p struct {
			Items []DownloadItem `json:"items"`
			Types []string       `json:"types"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("下载参数解析失败: %w", err)
		}
		return s.actionDownloadStart(p.Items, p.Types)
	case "download_cancel":
		return s.actionDownloadCancel()
	default:
		return nil, fmt.Errorf("未知动作: %s", req.Action)
	}
}

// actionStatus 汇总状态：登录态、任务进度、配置、下次执行时间。
func (s *Server) actionStatus() (any, error) {
	login, _ := s.loginStatus()
	s.mu.Lock()
	cfg := s.cfg
	st := s.state
	task := s.syncTask
	dlTask := s.downloadTask
	s.mu.Unlock()

	taskInfo := map[string]any{"running": false}
	if task != nil && task.isRunning() {
		taskInfo = task.snapshot()
	}
	// 下载任务状态（供页面展示进度）：任务对象存在时返回最近一次的终态快照
	dlInfo := map[string]any{"running": false}
	if dlTask != nil {
		dlInfo = dlTask.snapshot()
	}
	// 零值时间不返回，避免前端显示成 1/1/1
	var lastSync any
	if !st.LastSyncEnd.IsZero() {
		lastSync = st.LastSyncEnd
	}
	return map[string]any{
		"login":       login,
		"task":        taskInfo,
		"download":    dlInfo,
		"config":      cfg,
		"nextRun":     s.nextRunTime(cfg, st),
		"lastSync":    lastSync,
		"lastSyncOK":  st.LastSyncOK,
		"lastSyncMsg": st.LastSyncMsg,
		"trashCount":  len(s.readTrashIndex()),
	}, nil
}

// actionConfigSet 校验并保存配置。
func (s *Server) actionConfigSet(cfg Config) (any, error) {
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	s.mu.Lock()
	old := s.cfg
	// 路径锁定：同步范围必须钉死在固定目录，避免误改后抓到别的目录
	if old.LockPaths && (cfg.RemoteRoot != old.RemoteRoot || cfg.LocalRoot != old.LocalRoot) {
		s.mu.Unlock()
		return nil, errors.New("同步路径已锁定，不允许修改（如需调整请先关闭「锁定路径」）")
	}
	s.cfg = cfg
	err := s.saveConfigLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.audit("config_set remoteRoot=%q localRoot=%q schedule=%v/%s threads=%d",
		cfg.RemoteRoot, cfg.LocalRoot, cfg.Schedule.Enabled, cfg.Schedule.Mode, cfg.Threads)
	log.Printf("配置已更新（原 remoteRoot=%q）", old.RemoteRoot)
	return map[string]any{"saved": true, "config": cfg}, nil
}

// validateConfig 基础校验：路径必须绝对、并发有界、定时格式合法。
func validateConfig(cfg *Config) error {
	if !filepath.IsAbs(cfg.RemoteRoot) || !filepath.IsAbs(cfg.LocalRoot) {
		return errors.New("remoteRoot / localRoot 必须是绝对路径")
	}
	if strings.Contains(cfg.RemoteRoot, "..") || strings.Contains(cfg.LocalRoot, "..") {
		return errors.New("路径不允许包含 ..")
	}
	if cfg.Threads < 1 || cfg.Threads > 64 {
		return errors.New("threads 需在 1-64 之间")
	}
	if cfg.ParallelFiles < 1 || cfg.ParallelFiles > 16 {
		return errors.New("parallelFiles 需在 1-16 之间")
	}
	if cfg.BaidupcsBin == "" || !filepath.IsAbs(cfg.BaidupcsBin) {
		return errors.New("baidupcsBin 必须是绝对路径")
	}
	if cfg.TrashDir == "" || !filepath.IsAbs(cfg.TrashDir) {
		return errors.New("trashDir 必须是绝对路径")
	}
	if cfg.RetentionDays < 0 {
		return errors.New("retentionDays 不能为负")
	}
	if cfg.LogRetentionDays < 0 || cfg.LogRetentionDays > 365 {
		return errors.New("logRetentionDays 需在 0-365 之间（0 表示用默认 7 天）")
	}
	if cfg.LimitFiles < 0 || cfg.LimitFiles > 100000 {
		return errors.New("limitFiles 需在 0-100000 之间（0 表示不限制）")
	}
	// 在线搜索/下载相关校验：baseURL 必须指向回环，避免 SSRF
	{
		u, err := url.Parse(cfg.MusicApiBaseURL)
		if err != nil || u.Host == "" {
			return errors.New("musicApiBaseURL 格式不合法")
		}
		// 只允许回环：防止把下载/取直链指向外部主机（SSRF）
		host := u.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return errors.New("musicApiBaseURL 只允许回环地址（127.0.0.1 / localhost）")
		}
	}
	if cfg.MusicApiTimeoutSec < 5 || cfg.MusicApiTimeoutSec > 60 {
		return errors.New("musicApiTimeoutSec 需在 5-60 之间")
	}
	if cfg.DownloadDir == "" || !filepath.IsAbs(cfg.DownloadDir) {
		return errors.New("downloadDir 必须是绝对路径")
	}
	if strings.Contains(cfg.DownloadDir, "..") {
		return errors.New("downloadDir 不允许包含 ..")
	}
	if cfg.DownloadConcurrency < 1 || cfg.DownloadConcurrency > 8 {
		return errors.New("downloadConcurrency 需在 1-8 之间")
	}
	// 规范化下载类型
	dt := make([]string, 0, 3)
	seenDT := map[string]bool{}
	for _, t := range cfg.DownloadTypes {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "audio" && t != "lyric" && t != "cover" {
			continue
		}
		if seenDT[t] {
			continue
		}
		seenDT[t] = true
		dt = append(dt, t)
	}
	if len(dt) == 0 {
		dt = []string{"audio"}
	}
	cfg.DownloadTypes = dt
	// 规范化排除目录：去空格、去首尾斜杠、去重，只允许相对路径
	cleaned := make([]string, 0, len(cfg.ExcludeDirs))
	seen := map[string]bool{}
	for _, d := range cfg.ExcludeDirs {
		d = strings.Trim(strings.TrimSpace(d), "/")
		if d == "" || d == "." || strings.Contains(d, "..") {
			continue
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		cleaned = append(cleaned, d)
	}
	cfg.ExcludeDirs = cleaned
	if cfg.Schedule.Enabled {
		switch cfg.Schedule.Mode {
		case "daily":
			if _, _, err := parseHHMM(cfg.Schedule.At); err != nil {
				return fmt.Errorf("定时时刻格式错误: %w", err)
			}
		case "interval":
			if cfg.Schedule.Minutes < 5 {
				return errors.New("间隔同步的分钟数需 >= 5")
			}
		default:
			return errors.New(`schedule.mode 只能是 daily 或 interval`)
		}
	}
	return nil
}

// parseHHMM 解析 "HH:MM"。
func parseHHMM(v string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("应为 HH:MM 形式")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, errors.New("小时 0-23、分钟 0-59")
	}
	return h, m, nil
}

// actionLoginStatus 读取百度登录态（调用 baidupcs who）。
func (s *Server) actionLoginStatus() (any, error) {
	return s.loginStatus()
}

func (s *Server) loginStatus() (map[string]any, error) {
	s.mu.Lock()
	bin := s.cfg.BaidupcsBin
	s.mu.Unlock()

	out, err := runCmd(20*time.Second, nil, bin, "who")
	res := map[string]any{"loggedIn": false, "raw": strings.TrimSpace(string(out)), "error": ""}
	if err != nil {
		res["error"] = err.Error()
		return res, nil
	}
	// 输出形如：当前帐号 uid: 807177650, 用户名: xxx, ...
	uid := parseUID(strings.TrimSpace(string(out)))
	if uid != "" && uid != "0" {
		res["loggedIn"] = true
		res["uid"] = uid
		if name := parseUserName(strings.TrimSpace(string(out))); name != "" {
			res["userName"] = name
		}
	}
	return res, nil
}

func parseUID(s string) string {
	i := strings.Index(s, "uid:")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len("uid:"):])
	if j := strings.IndexAny(rest, ", \t"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

func parseUserName(s string) string {
	i := strings.Index(s, "用户名:")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(s[i+len("用户名:"):])
	if j := strings.IndexAny(rest, ",\t"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// runCmd 带超时执行外部命令；env 追加环境变量。
func runCmd(timeout time.Duration, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.CombinedOutput()
}
