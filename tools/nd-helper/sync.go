package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 同步日志按天分文件存放，避免单文件无限增长（打开慢/卡死）。
// 例：/var/log/baidusync/2026-09-27.log
const (
	syncLogDir        = "/var/log/baidusync"
	legacySyncLogPath = "/var/log/baidusync.log"
	logRetentionDays  = 7 // 默认保留天数（可被配置覆盖）
)

// syncLogPathFor 返回某天的日志文件路径。
func syncLogPathFor(t time.Time) string {
	return filepath.Join(syncLogDir, t.Format("2006-01-02")+".log")
}

// openSyncLog 打开（必要时创建）当天日志文件。
func openSyncLog(t time.Time) (*os.File, error) {
	if err := os.MkdirAll(syncLogDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	return os.OpenFile(syncLogPathFor(t), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
}

// urlRe 用于把日志里的超长下载直链压缩成 <url>，否则单行可达 2KB。
var urlRe = regexp.MustCompile(`https?://\S+`)

// noiseRe 命中即丢弃的行（baidupcs 的逐文件元数据块，占日志体积绝大部分）。
var noiseRe = regexp.MustCompile(`md5 |app_id|fs_id|创建日期|修改日期|文件名称|文件路径|文件大小|^\s*类型|^\s*-+\s*$`)

// filterLogLine 过滤/压缩一行日志；返回空串表示丢弃。
func filterLogLine(line string) string {
	line = cleanLogLine(strings.TrimRight(line, "\r"))
	if line == "" {
		return ""
	}
	if noiseRe.MatchString(line) {
		return ""
	}
	line = urlRe.ReplaceAllString(line, "<url>")
	return truncate(line, 600)
}

// cleanupOldLogs 删除保留期之外的按天日志文件（0 表示不限制）。
func cleanupOldLogs(keepDays int) {
	if keepDays <= 0 {
		keepDays = logRetentionDays
	}
	entries, err := os.ReadDir(syncLogDir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		day, perr := time.ParseInLocation("2006-01-02", strings.TrimSuffix(e.Name(), ".log"), time.Local)
		if perr != nil {
			continue // 非日期命名的日志不动
		}
		if day.Before(cutoff) {
			p := filepath.Join(syncLogDir, e.Name())
			if rerr := os.Remove(p); rerr == nil {
				log.Printf("[nd-helper] 已清理过期日志 %s（保留 %d 天）", e.Name(), keepDays)
			}
		}
	}
}

var audioExts = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true, ".aac": true,
	".wav": true, ".ogg": true, ".opus": true, ".wma": true, ".ape": true,
}

var lyricExts = map[string]bool{".lrc": true}
var coverExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// SyncTask 表示一次同步任务（同一时刻只允许一个）。
type SyncTask struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	running   bool
	mode      string
	trigger   string // manual | schedule
	types     []string
	startedAt time.Time
	finished  time.Time
	errMsg    string
	// stage 当前阶段（starting/preflight/enumerate/download/done/failed），
	// stageAt 该阶段的开始时间。同步里有「枚举远端目录」「文件已存在被跳过」这类长时间
	// 计数不变化的阶段，只暴露 filesDone/total 会让用户误以为任务卡死，因此额外上报阶段与阶段耗时。
	stage   string
	stageAt time.Time
	// dirsTotal/dirsDone 目录级进度：下载阶段按「远端父目录」分批执行，用目录数反映真实推进。
	dirsTotal int
	dirsDone  int
	// total 本次计划同步的文件数；logStart 本次开始的日志偏移；logPath 本次的日志文件。
	total    int
	logStart int64
	logPath  string
}

// setPlan 记录本次运行的计划数与日志起点。
func (t *SyncTask) setPlan(total int, logStart int64) {
	t.mu.Lock()
	t.total = total
	t.logStart = logStart
	t.mu.Unlock()
}

// setStage 切换当前阶段（供前端显示「正在做什么」，避免看起来像卡死）。
func (t *SyncTask) setStage(stage string) {
	t.mu.Lock()
	t.stage = stage
	t.stageAt = time.Now()
	t.mu.Unlock()
}

// setDirs 记录下载阶段的目录总数。
func (t *SyncTask) setDirs(total int) {
	t.mu.Lock()
	t.dirsTotal = total
	t.mu.Unlock()
}

// incDir 完成一个目录的下载批次后推进目录进度。
func (t *SyncTask) incDir() {
	t.mu.Lock()
	t.dirsDone++
	t.mu.Unlock()
}

func (t *SyncTask) isRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// snapshot 返回给前端的进度快照。
//
// 只统计「本次运行」的日志区间，并按唯一文件路径去重 —— 曾经的实现统计整个日志文件的行数，
// 会把历次运行与重试都累加进去（用户看到 2037 这种远大于实际曲库的数字）。
func (t *SyncTask) snapshot() map[string]any {
	t.mu.Lock()
	total, logStart, logPath := t.total, t.logStart, t.logPath
	t.mu.Unlock()
	done, skipped := countProgressSince(logPath, logStart)

	t.mu.Lock()
	defer t.mu.Unlock()
	stageElapsed := 0
	if !t.stageAt.IsZero() {
		stageElapsed = int(time.Since(t.stageAt).Seconds())
	}
	res := map[string]any{
		"running":   t.running,
		"mode":      t.mode,
		"trigger":   t.trigger,
		"types":     t.types,
		"stage":     t.stage,
		"startedAt": t.startedAt,
		"elapsed":   int(time.Since(t.startedAt).Seconds()),
		// filesProcessed = 已下载 + 已跳过：增量同步下绝大多数文件都是「已存在跳过」，
		// 只用 filesDone 会让进度长时间停在 0/N（用户以为卡死）。
		"filesDone":      done,
		"filesSkipped":   skipped,
		"filesProcessed": done + skipped,
		"filesSeen":      total,
		"dirsDone":       t.dirsDone,
		"dirsTotal":      t.dirsTotal,
		"stageElapsed":   stageElapsed,
		"errMsg":         t.errMsg,
	}
	if !t.running && !t.finished.IsZero() {
		res["finishedAt"] = t.finished
	}
	return res
}

// countDoneSince 统计 from 之后新增日志里「下载完成」的唯一文件数。
// 保留该函数是为了兼容既有单测，实际统计逻辑见 countProgressSince。
func countDoneSince(path string, from int64) int {
	done, _ := countProgressSince(path, from)
	return done
}

// countProgressSince 统计 from 之后新增日志里「已下载」与「已跳过（文件已存在）」的唯一文件数。
//
// 为什么要单独统计跳过数：增量同步时本地已是最新，baidupcs 对每个文件都会输出「文件已经存在…跳过」，
// 这些文件其实已经被处理过，不计入进度就会出现「进度长时间 0/425」的假卡死现象。
func countProgressSince(path string, from int64) (done int, skipped int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return 0, 0
	}
	donePaths := map[string]bool{}
	skipPaths := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if idx := strings.Index(line, "保存位置: "); idx >= 0 {
			if p := strings.TrimSpace(line[idx+len("保存位置: "):]); p != "" {
				donePaths[p] = true
			}
		}
		// 跳过行形如「[1] 文件已经存在: /opt/navidrome/music/xx.lrc, 跳过...」，
		// 用「, 跳过」截断尾部提示，只取文件路径去重。
		if idx := strings.Index(line, "文件已经存在: "); idx >= 0 {
			p := line[idx+len("文件已经存在: "):]
			if cut := strings.Index(p, ","); cut >= 0 {
				p = p[:cut]
			}
			if p = strings.TrimSpace(p); p != "" {
				skipPaths[p] = true
			}
		}
	}
	return len(donePaths), len(skipPaths)
}

// countProgress 统计日志里的下载完成/准备下载条数，给出粗略进度。
func countProgress(path string) (done int, seen int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "准备下载") {
			seen++
		}
		if strings.Contains(line, "下载完成") {
			done++
		}
	}
	return done, seen
}

// actionSyncStart 启动同步（manual 触发）。
func (s *Server) actionSyncStart(mode string, types []string, trigger string) (any, error) {
	s.mu.Lock()
	cfg := s.cfg
	if s.syncTask != nil && s.syncTask.isRunning() {
		s.mu.Unlock()
		return nil, errors.New("已有同步任务在运行，请先等待或取消")
	}
	if !cfg.Enabled {
		s.mu.Unlock()
		return nil, errors.New("同步功能已在配置中关闭（enabled=false）")
	}
	if mode == "" {
		mode = "incremental"
	}
	if len(types) == 0 {
		types = defaultTypes(cfg)
	}
	task := &SyncTask{mode: mode, trigger: trigger, types: types, startedAt: time.Now(), running: true,
		stage: "starting", stageAt: time.Now()}
	// 一开始就把日志起点与日志文件定下来：枚举阶段（尚未知道计划数）也不能把历次运行算进来
	task.logPath = syncLogPathFor(time.Now())
	task.logStart = fileSize(task.logPath)
	s.syncTask = task
	s.state.LastSyncStart = task.startedAt
	s.saveStateLocked()
	s.mu.Unlock()

	s.audit("sync_start mode=%s types=%v trigger=%s", mode, types, trigger)
	go s.runSync(task, cfg)
	return map[string]any{"started": true, "mode": mode, "types": types, "startedAt": task.startedAt}, nil
}

func defaultTypes(cfg Config) []string {
	types := []string{"song"}
	if cfg.IncludeLyrics {
		types = append(types, "lyric")
	}
	if cfg.IncludeCovers {
		types = append(types, "cover")
	}
	return types
}

// runSync 执行同步任务（goroutine）。
func (s *Server) runSync(task *SyncTask, cfg Config) {
	err := s.execSync(task, cfg)

	task.mu.Lock()
	task.running = false
	task.finished = time.Now()
	if err != nil {
		task.errMsg = err.Error()
	}
	task.mu.Unlock()
	if err != nil {
		task.setStage("failed")
	} else {
		task.setStage("done")
	}

	s.mu.Lock()
	s.state.LastSyncEnd = task.finished
	s.state.LastSyncOK = err == nil
	if err != nil {
		s.state.LastSyncMsg = err.Error()
	} else {
		s.state.LastSyncMsg = "同步完成"
		// 手动同步成功也算“今天已同步”，否则调度器会立刻再补跑一次
		// （daily 模式靠 LastRunDay 判重，官方 03:00 定时与手动同步会打架）。
		s.state.LastRunDay = task.finished.Format("2006-01-02")
		s.state.LastRunTime = task.finished
	}
	s.saveStateLocked()
	s.mu.Unlock()

	if err != nil {
		s.audit("sync_end FAILED err=%v", err)
		log.Printf("同步结束（失败）: %v", err)
		return
	}
	// 下载产物属主归位（助手以 root 运行，未归位会导致服务端/扫描器权限异常）
	chownTree(cfg.LocalRoot)
	s.audit("sync_end OK mode=%s", task.mode)
	log.Printf("同步结束（成功），共完成 %d 个文件", func() int { d, _ := countProgress(task.logPath); return d }())
}

// execSync 组装并执行 baidupcs 下载（含可选的文件类型过滤）。
func (s *Server) execSync(task *SyncTask, cfg Config) error {
	if err := os.MkdirAll(cfg.LocalRoot, 0o755); err != nil {
		return fmt.Errorf("创建音乐目录失败: %w", err)
	}
	if _, err := os.Stat(cfg.BaidupcsBin); err != nil {
		return fmt.Errorf("未找到 baidupcs 可执行文件 %s: %w", cfg.BaidupcsBin, err)
	}
	// 要求远端与本地根目录同名，才能把远端相对目录直接映射到本地相对目录
	remoteBase := filepath.Base(filepath.Clean(cfg.RemoteRoot))
	localBase := filepath.Base(filepath.Clean(cfg.LocalRoot))
	if remoteBase != localBase {
		return fmt.Errorf("远端目录名 %q 与本地音乐目录名 %q 不一致，无法直接映射（请调整配置）", remoteBase, localBase)
	}
	// 预检远端目录：baidupcs 遇到不存在的目录或网络故障时，download 会「零输出 + 退出码 0」，
	// 只靠事后扫日志会把这种情况误判成“同步完成”，所以同步前先 ls 一次探明。
	task.setStage("preflight")
	if err := s.preflightRemote(cfg); err != nil {
		return err
	}

	logFileName := syncLogPathFor(time.Now())
	task.logPath = logFileName
	logFile, err := openSyncLog(time.Now())
	if err != nil {
		return err
	}
	defer logFile.Close()
	// 记录本次运行前的日志长度，运行结束后只扫描新增部分（避免历史错误误报）
	startSize := int64(0)
	if fi, statErr := logFile.Stat(); statErr == nil {
		startSize = fi.Size()
	}
	fmt.Fprintf(logFile, "\n===== 同步开始 %s mode=%s types=%v =====\n",
		time.Now().Format("2006-01-02 15:04:05"), task.mode, task.types)

	// 同步范围严格限制在「远端根目录内的歌曲/歌词/封面」：
	// 先枚举远端文件，排除配置里指定的目录（如 attachments），再按扩展名白名单筛选，
	// 绝不整目录抓取，避免把无关文件带进媒体库。
	// 枚举远端目录可能耗时较久（整棵目录树），必须先切阶段，前端才能显示「正在枚举」而不是静默不动。
	task.setStage("enumerate")
	paths, err := s.listRemoteFiles(cfg)
	if err != nil {
		return err
	}
	scoped, skippedDir := filterOutDirs(paths, cfg)
	wanted := filterByTypes(scoped, cfg, task.types)
	matched := len(wanted)
	// 单次同步文件数上限（limitFiles，默认 0 = 不限）：验证阶段只跑十几首，不必整库扫一遍。
	// 正式运行务必保持 0，否则排在后面的新歌永远同步不到。
	limitNote := ""
	if cfg.LimitFiles > 0 && len(wanted) > cfg.LimitFiles {
		wanted = wanted[:cfg.LimitFiles]
		limitNote = fmt.Sprintf("，按 limitFiles=%d 限制为前 %d 个", cfg.LimitFiles, len(wanted))
	}
	fmt.Fprintf(logFile, "范围：远端共 %d 个文件，排除目录跳过 %d 个，符合歌曲/歌词/封面要求的 %d 个%s\n",
		len(paths), skippedDir, matched, limitNote)
	// 本次运行的进度基准：计划总数 + 日志起点（前端只显示本次运行的真实数字）
	task.setPlan(len(wanted), startSize)
	if len(wanted) == 0 {
		return nil
	}

	// 按「远端父目录」分组下载：baidupcs 用 --saveto + 文件名保存，
	// 所以必须把 --saveto 指到与远端目录对应的本地目录，
	// 否则文件会落到 localRoot 的上一级（曾出现 /opt/navidrome/xxx.mp3 这种脏数据）。
	groups := map[string][]string{}
	for _, p := range wanted {
		dir := pathpkg.Dir(p)
		groups[dir] = append(groups[dir], p)
	}
	dirs := make([]string, 0, len(groups))
	for d := range groups {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs) // 输出稳定，便于排查
	task.setStage("download")
	task.setDirs(len(dirs))

	const batch = 100
	for _, dir := range dirs {
		files := groups[dir]
		localDir := cfg.LocalRoot
		if rel, relErr := relTo(cfg.RemoteRoot, dir); relErr == nil && rel != "" && rel != "." {
			localDir = filepath.Join(cfg.LocalRoot, filepath.FromSlash(rel))
		}
		if err := os.MkdirAll(localDir, 0o755); err != nil {
			return fmt.Errorf("创建本地目录 %s 失败: %w", localDir, err)
		}
		for i := 0; i < len(files); i += batch {
			end := i + batch
			if end > len(files) {
				end = len(files)
			}
			args := []string{"download", "--mode=locate", "--saveto=" + localDir,
				"-p", fmt.Sprint(cfg.Threads), "-l", fmt.Sprint(cfg.ParallelFiles)}
			if task.mode == "full" {
				args = append(args, "--ow")
			}
			args = append(args, files[i:end]...)
			if err := s.execCmd(task, logFile, cfg.BaidupcsBin, args...); err != nil {
				return err
			}
			task.incDir() // 每个批次完成即推进目录进度
		}
	}
	return scanNewLogErrors(task.logPath, startSize)
}

// filterOutDirs 去掉位于排除目录（相对远端根的前缀）下的文件，返回剩余路径与被排除的数量。
func filterOutDirs(paths []string, cfg Config) ([]string, int) {
	if len(cfg.ExcludeDirs) == 0 {
		return paths, 0
	}
	root := strings.TrimSuffix(cfg.RemoteRoot, "/")
	prefixes := make([]string, 0, len(cfg.ExcludeDirs))
	for _, d := range cfg.ExcludeDirs {
		prefixes = append(prefixes, root+"/"+d+"/")
	}
	res := make([]string, 0, len(paths))
	skipped := 0
	for _, p := range paths {
		skip := false
		for _, pre := range prefixes {
			// 前缀匹配 + 完全相等（该目录本身）都算命中
			if strings.HasPrefix(p, pre) || p == strings.TrimSuffix(pre, "/") {
				skip = true
				break
			}
		}
		if skip {
			skipped++
			continue
		}
		res = append(res, p)
	}
	return res, skipped
}

// fatalLogPatterns 这些日志行意味着 baidupcs 根本没跑起来或列不到文件，
// 此时它仍会以退出码 0 结束（打印「数据总量: 0B」），必须显式当失败处理，
// 否则定时任务会反复“假成功”，而库里其实一个新文件都没同步下来。
var fatalLogPatterns = []string{
	"获取目录下的文件列表",
	"user is not authorized",
	"尚未登录",
	"未登录",
	"登录失败",
	"Cookie",
}

// transientNetPatterns 网络类错误：单文件重试失败很常见（例如 CDN 域名解析到本机不可达的 IPv6），
// 因此只在“本次一个文件都没下成”时才判失败，避免把偶发重试误报成同步失败。
var transientNetPatterns = []string{
	"network is unreachable",
	"cannot assign requested address",
	"no route to host",
	"i/o timeout",
	"connection reset by peer",
}

// remoteErrorMarkers 远端列目录失败时 baidupcs 输出的特征串。
var remoteErrorMarkers = []string{
	"获取目录下的文件列表",
	"文件不存在",
	"遇到错误",
	"网络错误",
	"user is not authorized",
	"尚未登录",
}

// preflightRemote 同步前预检远端目录是否可列出且非空。
//
// 必要性：baidupcs 的 download 遇到不存在的目录/网络故障时会输出「数据总量: 0B」
// 并以退出码 0 结束，事后无法与「确实没有新文件」区分，只能提前探明。
func (s *Server) preflightRemote(cfg Config) error {
	out, err := runCmd(2*time.Minute, nil, cfg.BaidupcsBin, "ls", cfg.RemoteRoot)
	text := string(out)
	if err != nil {
		return fmt.Errorf("网盘目录预检失败: %w（%s）", err, truncate(strings.TrimSpace(text), 160))
	}
	for _, p := range remoteErrorMarkers {
		if strings.Contains(text, p) {
			return fmt.Errorf("网盘目录不可用（%s）: %s", p, truncate(cleanLogLine(text), 200))
		}
	}
	if countLsEntries(text) == 0 {
		return fmt.Errorf("网盘目录 %s 列表为空或无法访问，请检查路径与登录状态", cfg.RemoteRoot)
	}
	return nil
}

// countLsEntries 统计 baidupcs ls 输出里的条目数（表格数据行形如「序号  大小  日期  名称」）。
func countLsEntries(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, "  ") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}
		// 首列必须是序号数字（表头是 "#"，分隔行是 "----"）
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue
		}
		// 第三列是修改日期（yyyy-mm-dd）
		if !strings.Contains(fields[2], "-") {
			continue
		}
		n++
	}
	return n
}

// scanNewLogErrors 扫描本次运行新增的日志并判定成败。
func scanNewLogErrors(path string, from int64) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil
	}
	var firstNet string
	done := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "下载完成") {
			done++
		}
		for _, p := range fatalLogPatterns {
			if strings.Contains(line, p) {
				// 只保留一行摘要，避免把整段日志塞进错误消息
				return fmt.Errorf("baidupcs 报告错误（%s）: %s", p, truncate(strings.TrimSpace(line), 200))
			}
		}
		if firstNet == "" {
			for _, p := range transientNetPatterns {
				if strings.Contains(line, p) {
					firstNet = truncate(strings.TrimSpace(line), 200)
					break
				}
			}
		}
	}
	if firstNet != "" && done == 0 {
		return fmt.Errorf("baidupcs 下载失败（网络不可达）: %s", firstNet)
	}
	return nil
}

// execCmd 启动子进程，把输出**过滤后**写入日志（省体积：丢弃逐文件元数据、压缩超长直链），
// 支持取消（杀进程组）。
func (s *Server) execCmd(task *SyncTask, logFile *os.File, bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = newSysProcAttr() // 平台差异：Linux 下独立进程组，便于整组终止

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // 合并到同一条管道，避免两路交错

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if line := filterLogLine(sc.Text()); line != "" {
				fmt.Fprintln(logFile, line)
			}
		}
	}()

	task.mu.Lock()
	task.cmd = cmd
	task.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return err
	}
	err = cmd.Wait()
	// 等待过滤协程把剩余输出写完（进程退出后管道随即关闭，正常很快）
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	task.mu.Lock()
	cancelled := task.cmd == nil // cancel 时被置空
	task.cmd = nil
	task.mu.Unlock()

	if cancelled {
		return errors.New("任务已被取消")
	}
	return err
}

// listRemoteFiles 递归枚举远端文件（解析 baidupcs tree 输出）。
func (s *Server) listRemoteFiles(cfg Config) ([]string, error) {
	out, err := runCmd(10*time.Minute, nil, cfg.BaidupcsBin, "tree", cfg.RemoteRoot)
	text := string(out)
	if err != nil {
		return nil, fmt.Errorf("枚举远端目录失败: %w (%s)", err, strings.TrimSpace(text))
	}
	for _, p := range remoteErrorMarkers {
		if strings.Contains(text, p) {
			return nil, fmt.Errorf("枚举远端目录失败（%s）: %s", p, truncate(cleanLogLine(text), 200))
		}
	}
	paths := parseRemotePaths(text, cfg.RemoteRoot)
	// 预检已确认目录非空，这里再为空就是解析/接口异常 —— 必须报错，
	// 否则会退化成“同步完成但没有同步任何文件”的静默空跑。
	if len(paths) == 0 {
		return nil, errors.New("远端枚举结果为空（预检显示目录非空），请检查 baidupcs 输出与解析逻辑")
	}
	return paths, nil
}

var pathTokenRe = regexp.MustCompile(`[^\s|]+`)

// baidupcs tree 的缩进标记（BaiduPCS-Go 只输出相对名，没有全路径参数）。
const (
	treeContinue = "│   " // 上层仍有后续兄弟节点
	treeBlank    = "    " // 上层已结束
	treeBranch   = "├── "
	treeLast     = "└── "
)

// parseRemotePaths 把 `baidupcs tree` 的树形输出还原成完整远端文件路径列表。
//
// 输出形如（相对名 + 缩进）：
//
//	├── Acro Jazz Laboratories/
//	│   ├── acro jazz one/
//	│   │   └── Realize - Acro Jazz Laboratories.mp3
//
// 因此用「父目录栈 + 深度」重建相对路径，再拼回 remoteRoot 前缀；
// 目录行以 "/" 结尾，据此维护栈。历史上曾按绝对路径前缀匹配，导致恒返回 0 个文件
// （同步静默空跑），这里必须按缩进解析。
func parseRemotePaths(out, remoteRoot string) []string {
	root := strings.TrimSuffix(remoteRoot, "/")
	seen := map[string]bool{}
	var res []string
	var stack []string // stack[d] = 深度 d 处的目录名

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 兼容旧输出：若整行本身就是绝对路径，直接采用
		if tok := strings.Trim(strings.TrimSpace(line), "\"'"); strings.HasPrefix(tok, root+"/") {
			if !strings.HasSuffix(tok, "/") && !seen[tok] {
				seen[tok] = true
				res = append(res, tok)
			}
			continue
		}

		rest := line
		depth := 0
		for {
			if strings.HasPrefix(rest, treeContinue) {
				rest = rest[len(treeContinue):]
				depth++
				continue
			}
			if strings.HasPrefix(rest, treeBlank) {
				rest = rest[len(treeBlank):]
				depth++
				continue
			}
			break
		}
		if strings.HasPrefix(rest, treeBranch) {
			rest = rest[len(treeBranch):]
		} else if strings.HasPrefix(rest, treeLast) {
			rest = rest[len(treeLast):]
		} else if depth == 0 {
			// 顶层的孤立行（例如 "[ undefined]" 之类的噪音），忽略
			continue
		}
		name := strings.TrimSpace(rest)
		if name == "" {
			continue
		}

		if strings.HasSuffix(name, "/") {
			// 目录：截断到当前深度后压栈
			dir := strings.TrimSuffix(name, "/")
			if depth < len(stack) {
				stack = stack[:depth]
			}
			stack = append(stack, dir)
			continue
		}
		// 文件：路径 = 祖先目录 + 文件名
		parts := make([]string, 0, depth+1)
		for i := 0; i < depth && i < len(stack); i++ {
			parts = append(parts, stack[i])
		}
		parts = append(parts, name)
		full := root + "/" + strings.Join(parts, "/")
		if !seen[full] {
			seen[full] = true
			res = append(res, full)
		}
	}
	return res
}

// filterByTypes 按用户选择的类型过滤远端文件路径。
func filterByTypes(paths []string, cfg Config, types []string) []string {
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	var res []string
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		switch {
		case audioExts[ext]:
			if want["song"] {
				res = append(res, p)
			}
		case lyricExts[ext]:
			if want["lyric"] {
				res = append(res, p)
			}
		case coverExts[ext]:
			if want["cover"] {
				res = append(res, p)
			}
		}
	}
	return res
}

// actionSyncCancel 取消当前同步任务（终止进程组）。
func (s *Server) actionSyncCancel() (any, error) {
	s.mu.Lock()
	task := s.syncTask
	s.mu.Unlock()
	if task == nil || !task.isRunning() {
		return map[string]any{"cancelled": false, "reason": "当前没有运行中的任务"}, nil
	}

	task.mu.Lock()
	cmd := task.cmd
	task.cmd = nil // execCmd 会据此判定为"已取消"
	task.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		pid := cmd.Process.Pid
		terminateGroup(pid) // 先优雅终止
		time.Sleep(2 * time.Second)
		if processAlive(pid) {
			killGroup(pid) // 仍未退出则强杀
		}
	}
	s.audit("sync_cancel")
	return map[string]any{"cancelled": true}, nil
}

// actionLogTail 返回同步日志末尾若干行。
//
// 实现要点：从文件末尾倒读固定大小的块（O(1) 级别），而不是整文件扫描 ——
// 之前日志越大读取越慢，页面每 5 秒轮询一次会造成明显卡顿。
func (s *Server) actionLogTail(lines int, date string) (any, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	day := time.Now()
	if date != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", date, time.Local); err == nil {
			day = parsed
		}
	}
	path := syncLogPathFor(day)
	buf, truncated := readTailLines(path, lines)
	out := make([]string, 0, len(buf))
	for _, line := range buf {
		if l := filterLogLine(line); l != "" {
			out = append(out, l)
		}
	}
	return map[string]any{
		"lines":     out,
		"date":      day.Format("2006-01-02"),
		"file":      filepath.Base(path),
		"truncated": truncated,
		"files":     listLogFiles(),
	}, nil
}

// readTailLines 倒读文件末尾最多 max 行；第二个返回值表示是否还有更早内容未返回。
func readTailLines(path string, max int) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil, false
	}
	const chunk = 64 * 1024
	var (
		data      []byte
		offset    = fi.Size()
		collected int
	)
	for offset > 0 {
		readSize := int64(chunk)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
			break
		}
		data = append(buf, data...)
		collected = strings.Count(string(data), "\n")
		// 多读一行用于判断是否还有更早内容
		if collected > max+1 || offset == 0 {
			break
		}
	}
	text := string(data)
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if offset > 0 && len(all) > max {
		all = all[len(all)-max:]
		return all, true
	}
	if len(all) > max {
		all = all[len(all)-max:]
	}
	return all, offset > 0
}

// listLogFiles 返回现有日志日期（按日期倒序，便于前端切换查看）。
// 只返回能解析成日期的文件，归档文件（如 legacy.log）不参与选择。
func listLogFiles() []string {
	entries, err := os.ReadDir(syncLogDir)
	if err != nil {
		return []string{}
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".log")
		if _, perr := time.ParseInLocation("2006-01-02", name, time.Local); perr != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > 30 { // 前端只用于选择，最多返回 30 天
		names = names[:30]
	}
	return names
}

// logNoise 是 baidupcs 在非 TTY 环境下输出的一些无意义片段。
var logNoise = []string{"[ undefined]", "\x1b"}

// cleanLogLine 去掉终端控制序列与噪音标记，只保留可读内容。
func cleanLogLine(s string) string {
	s = strings.ReplaceAll(s, "[ undefined]", "")
	// 去掉 ANSI 转义序列（如 \x1b[0m）
	if strings.Contains(s, "\x1b") {
		s = ansiRe.ReplaceAllString(s, "")
	}
	return strings.TrimSpace(s)
}

var ansiRe = regexp.MustCompile("\\x1b\\[[0-9;]*[A-Za-z]")

// ---------------------------------------------------------------------------
// 内置调度器（读配置，页面改动即时生效，无需重载 systemd）
// ---------------------------------------------------------------------------

func (s *Server) scheduleLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastCleanupDay := ""
	for range ticker.C {
		s.mu.Lock()
		cfg := s.cfg
		st := s.state
		s.mu.Unlock()

		// 每天清理一次过期日志（日志按天分文件的保留策略）
		today := time.Now().Format("2006-01-02")
		if lastCleanupDay != today {
			lastCleanupDay = today
			go cleanupOldLogs(cfg.LogRetentionDays)
		}

		if !cfg.Enabled || !cfg.Schedule.Enabled {
			continue
		}
		if task := s.syncTask; task != nil && task.isRunning() {
			continue
		}
		if !s.dueToRun(cfg, st) {
			continue
		}

		log.Printf("定时同步触发（%s/%v）", cfg.Schedule.Mode, cfg.Schedule.At)
		if _, err := s.actionSyncStart("incremental", nil, "schedule"); err != nil {
			log.Printf("定时同步启动失败: %v", err)
			continue
		}
		s.mu.Lock()
		if cfg.Schedule.Mode == "daily" {
			s.state.LastRunDay = time.Now().Format("2006-01-02")
		}
		s.state.LastRunTime = time.Now()
		s.saveStateLocked()
		s.mu.Unlock()
	}
}

// dueToRun 判断当前是否到点。
func (s *Server) dueToRun(cfg Config, st State) bool {
	now := time.Now()
	switch cfg.Schedule.Mode {
	case "daily":
		h, m, err := parseHHMM(cfg.Schedule.At)
		if err != nil {
			return false
		}
		today := now.Format("2006-01-02")
		if st.LastRunDay == today {
			return false
		}
		// 允许 10 分钟误差窗口（进程重启/休眠后仍能补跑）
		target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		return now.After(target) && now.Sub(target) < 12*time.Hour
	case "interval":
		if cfg.Schedule.Minutes < 5 {
			return false
		}
		if st.LastRunTime.IsZero() {
			return false
		}
		return now.Sub(st.LastRunTime) >= time.Duration(cfg.Schedule.Minutes)*time.Minute
	}
	return false
}

// nextRunTime 计算下次执行时间（用于前端展示）。
func (s *Server) nextRunTime(cfg Config, st State) any {
	if !cfg.Enabled || !cfg.Schedule.Enabled {
		return nil
	}
	now := time.Now()
	switch cfg.Schedule.Mode {
	case "daily":
		h, m, err := parseHHMM(cfg.Schedule.At)
		if err != nil {
			return nil
		}
		next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		return next
	case "interval":
		if st.LastRunTime.IsZero() {
			return now
		}
		next := st.LastRunTime.Add(time.Duration(cfg.Schedule.Minutes) * time.Minute)
		if next.Before(now) {
			next = now
		}
		return next
	}
	return nil
}
