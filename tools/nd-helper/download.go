package main

// 在线音乐搜索下载（对接 go-music-api）
//
// 设计要点（安全优先）：
//  1. 助手只接收歌曲 id/source，**不接收任意 URL** —— 播放直链由助手自行向 go-music-api 换取，
//     杜绝 SSRF 与「任意文件写入」。
//  2. baseURL 强制为回环地址（validateConfig 中校验），不允许指向外部主机。
//  3. 落点固定在 cfg.DownloadDir 之内，写入前做「包含性校验」（ensureWithin）。
//  4. 文件名做白名单清洗（剔除路径分隔符、控制字符等），限长，避免目录穿越。
//  5. 增量：目标文件已存在且大小 > 0 且未开启覆盖 → 跳过并计数。
//  6. 先写 .part 临时文件、校验通过后再 rename —— 避免半截文件被 Navidrome Watcher 扫进库。
//  7. 复用既有按天日志与审计日志，便于事后核查。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxDownloadItems = 200       // 单次提交上限，防止误操作把服务器拖垮
	minAudioBytes    = 64 * 1024 // 小于此值视为失败（避免把错误页当音频存下来）
	maxNameLen       = 120       // 单个文件名片段最大长度
	downloadLogTag   = "[download] "
)

// DownloadItem 一首待下载歌曲，由 Navidrome 侧从搜索结果中挑选后传入。
type DownloadItem struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Name     string `json:"name"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Duration int    `json:"duration"`
	Ext      string `json:"ext"`
	CoverURL string `json:"coverUrl"`
}

// DownloadTask 一次下载任务（同一时刻只允许一个）。
type DownloadTask struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	running   bool
	types     []string
	total     int
	done      int
	skipped   int
	failed    int
	current   string
	startedAt time.Time
	finished  time.Time
	errMsg    string
	logPath   string
	errs      []string // 失败明细（最多保留 20 条，供页面提示）
}

func (t *DownloadTask) setTotal(n int) {
	t.mu.Lock()
	t.total = n
	t.mu.Unlock()
}

func (t *DownloadTask) setCurrent(name string) {
	t.mu.Lock()
	t.current = name
	t.mu.Unlock()
}

func (t *DownloadTask) incDone() {
	t.mu.Lock()
	t.done++
	t.mu.Unlock()
}

func (t *DownloadTask) incSkipped() {
	t.mu.Lock()
	t.skipped++
	t.mu.Unlock()
}

func (t *DownloadTask) addError(msg string) {
	t.mu.Lock()
	t.failed++
	if len(t.errs) < 20 {
		t.errs = append(t.errs, msg)
	}
	t.mu.Unlock()
}

func (t *DownloadTask) isRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// snapshot 返回给前端的进度快照。
func (t *DownloadTask) snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := 0
	if !t.startedAt.IsZero() {
		elapsed = int(time.Since(t.startedAt).Seconds())
	}
	res := map[string]any{
		"running":   t.running,
		"types":     t.types,
		"total":     t.total,
		"done":      t.done,
		"skipped":   t.skipped,
		"failed":    t.failed,
		"processed": t.done + t.skipped,
		"current":   t.current,
		"startedAt": t.startedAt,
		"elapsed":   elapsed,
		"errMsg":    t.errMsg,
		"errors":    t.errs,
	}
	if !t.running && !t.finished.IsZero() {
		res["finishedAt"] = t.finished
	}
	return res
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

// validMusicSources 支持的平台标识（与 go-music-api 的 source 取值一致）。
// 用于在入口处拦住拼造出来的异常 source，避免拼出意外请求路径。
var validMusicSources = map[string]bool{
	"netease": true, "qq": true, "kugou": true, "kuwo": true, "migu": true,
	"qianqian": true, "fivesing": true, "jamendo": true, "joox": true,
	"bilibili": true, "soda": true,
}

// 文件名中不允许出现的字符（路径分隔符、Windows 保留字符、控制字符）
var unsafeNameRe = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f\x7f]`)

// sanitizeName 清洗文件/目录名片段：剔除危险字符、折叠空白、去首尾点与空格、限长。
// 目的：即便歌曲名含 "../" 或 "/" 也无法逃逸出目标目录。
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = unsafeNameRe.ReplaceAllString(s, "_")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if s == "" {
		s = "unknown"
	}
	if len(s) > maxNameLen {
		// 按字节截断后可能产生半个多字节字符（中文歌名常见），用 utf8 校验后回退
		s = s[:maxNameLen]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s = strings.TrimSpace(s)
	}
	if s == "" {
		s = "unknown"
	}
	return s
}

// extFromContentType 由 Content-Type 推断扩展名（搜索结果未给出 ext 时兜底）。
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch ct {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/opus":
		return ".opus"
	}
	return ""
}

// normalizeExt 规范化扩展名：只接受音频白名单内的扩展名。
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if audioExts[ext] {
		return ext
	}
	return ""
}

// containsTypes 判断 types 是否包含某类内容。
func containsTypes(types []string, want string) bool {
	if len(types) == 0 {
		return true // 未指定则全部下载
	}
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// newDownloadClient 下载专用 HTTP 客户端：分阶段超时，避免大文件下载被整体超时中断。
func newDownloadClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
	}
}

// ---------------------------------------------------------------------------
// 动作入口
// ---------------------------------------------------------------------------

// actionDownloadStart 提交下载任务。
func (s *Server) actionDownloadStart(items []DownloadItem, types []string) (any, error) {
	s.mu.Lock()
	cfg := s.cfg
	running := s.downloadTask != nil && s.downloadTask.isRunning()
	s.mu.Unlock()

	if !cfg.MusicApiEnabled {
		return nil, errors.New("在线搜索功能未启用（配置 musicApiEnabled）")
	}
	if running {
		return nil, errors.New("已有下载任务在进行中")
	}
	if len(items) == 0 {
		return nil, errors.New("未选择任何歌曲")
	}
	if len(items) > maxDownloadItems {
		return nil, fmt.Errorf("单次最多下载 %d 首（当前 %d 首）", maxDownloadItems, len(items))
	}
	// 逐项校验必填字段（id/source 是换取直链的唯一依据）
	for i, it := range items {
		if strings.TrimSpace(it.ID) == "" || strings.TrimSpace(it.Source) == "" {
			return nil, fmt.Errorf("第 %d 项缺少 id 或 source", i+1)
		}
		// source 白名单：只允许已知平台，避免拼出意外请求路径
		if !validMusicSources[strings.TrimSpace(it.Source)] {
			return nil, fmt.Errorf("第 %d 项的平台 %q 不受支持", i+1, it.Source)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	task := &DownloadTask{
		cancel:    cancel,
		running:   true,
		types:     types,
		total:     len(items),
		startedAt: time.Now(),
	}
	s.mu.Lock()
	s.downloadTask = task
	s.mu.Unlock()

	go s.runDownload(ctx, task, cfg, items, types)

	s.audit("download_start total=%d types=%v", len(items), types)
	log.Printf("%s任务已提交：共 %d 首，types=%v", downloadLogTag, len(items), types)
	return map[string]any{"started": true, "total": len(items)}, nil
}

// actionDownloadCancel 取消当前下载任务。
func (s *Server) actionDownloadCancel() (any, error) {
	s.mu.Lock()
	task := s.downloadTask
	s.mu.Unlock()
	if task == nil || !task.isRunning() {
		return nil, errors.New("当前没有正在进行的下载任务")
	}
	task.cancel()
	s.audit("download_cancel")
	log.Printf("%s收到取消请求", downloadLogTag)
	return map[string]any{"cancelled": true}, nil
}

// runDownload 顺序下载（并发数由 cfg.DownloadConcurrency 控制），单个失败不中断整体。
func (s *Server) runDownload(ctx context.Context, task *DownloadTask, cfg Config, items []DownloadItem, types []string) {
	client := newDownloadClient()
	logFile, _ := openSyncLog(time.Now())
	if logFile != nil {
		defer logFile.Close()
		task.mu.Lock()
		task.logPath = logFile.Name()
		task.mu.Unlock()
	}
	lg := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		log.Printf("%s%s", downloadLogTag, line)
		if logFile != nil {
			fmt.Fprintf(logFile, "%s %s\n", time.Now().Format("15:04:05"), line)
		}
	}

	// 确保落点目录存在（属主交给 navidrome，便于后续库维护）
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		s.finishDownload(task, fmt.Sprintf("创建下载目录失败: %v", err))
		return
	}
	chownToNavidrome(cfg.DownloadDir)

	concurrency := cfg.DownloadConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	// 封面按目录去重：同一目录只下一次 cover.jpg
	var coverMu sync.Mutex
	coverDone := map[string]bool{}

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(item DownloadItem) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			task.setCurrent(item.Artist + " - " + item.Name)
			res := s.downloadOne(ctx, client, cfg, item, types, lg, &coverMu, coverDone)
			switch res {
			case dlSkipped:
				task.incSkipped()
			case dlOK:
				task.incDone()
			case dlFailed:
				task.addError(item.Artist + " - " + item.Name)
			}
		}(it)
	}
	wg.Wait()

	// 统一修正属主（助手以 root 运行，落盘文件需归 navidrome，避免后续库操作受限）
	chownTree(cfg.DownloadDir)

	msg := ""
	if ctx.Err() != nil {
		msg = "下载已取消"
	}
	s.finishDownload(task, msg)
	snap := task.snapshot()
	s.audit("download_done done=%v skipped=%v failed=%v msg=%q", snap["done"], snap["skipped"], snap["failed"], msg)
	lg("任务结束：完成 %v，跳过 %v，失败 %v", snap["done"], snap["skipped"], snap["failed"])
}

// finishDownload 收尾：标记任务不再运行。
func (s *Server) finishDownload(task *DownloadTask, errMsg string) {
	task.mu.Lock()
	task.running = false
	task.finished = time.Now()
	task.errMsg = errMsg
	task.current = ""
	task.mu.Unlock()
}

type dlResult int

const (
	dlOK dlResult = iota
	dlSkipped
	dlFailed
)

// downloadOne 下载单首：音频 +（可选）歌词 +（可选）封面。
func (s *Server) downloadOne(ctx context.Context, client *http.Client, cfg Config,
	item DownloadItem, types []string, lg func(string, ...any),
	coverMu *sync.Mutex, coverDone map[string]bool) dlResult {

	// 1) 计算落点（先清洗命名，再做包含性校验）
	artistDir := filepath.Join(cfg.DownloadDir, sanitizeName(item.Artist))
	stem := sanitizeName(item.Artist) + " - " + sanitizeName(item.Name)

	ext := normalizeExt(item.Ext)
	if ext == "" {
		ext = ".mp3" // 兜底：多数平台返回 mp3
	}
	audioPath, err := ensureWithin(cfg.DownloadDir, filepath.Join(artistDir, stem+ext))
	if err != nil {
		lg("路径校验失败（跳过）: %s - %s (%v)", item.Artist, item.Name, err)
		return dlFailed
	}

	if err := os.MkdirAll(filepath.Dir(audioPath), 0o755); err != nil {
		lg("创建目录失败: %v", err)
		return dlFailed
	}

	// 2) 音频（增量：已存在且非空且未开覆盖 → 跳过）
	if containsTypes(types, "audio") {
		if st, statErr := os.Stat(audioPath); statErr == nil && st.Size() > 0 && !cfg.DownloadOverwrite {
			lg("已存在，跳过: %s", audioPath)
			// 音频已存在时，仍然补一次歌词/封面（可能上次只下到音频）
		} else {
			if err := s.fetchStream(ctx, client, cfg, item, audioPath, lg); err != nil {
				lg("音频下载失败: %s - %s (%v)", item.Artist, item.Name, err)
				return dlFailed
			}
			lg("音频完成: %s", audioPath)
		}
	}

	// 3) 歌词：同名 .lrc（Navidrome 会自动读取同目录同名歌词）
	if containsTypes(types, "lyric") {
		lrcPath := strings.TrimSuffix(audioPath, ext) + ".lrc"
		if st, statErr := os.Stat(lrcPath); statErr == nil && st.Size() > 0 && !cfg.DownloadOverwrite {
			lg("歌词已存在，跳过: %s", lrcPath)
		} else if err := s.fetchLyric(ctx, client, cfg, item, lrcPath); err != nil {
			// 歌词失败不算整首失败（很多老歌本来就没有歌词）
			lg("歌词未获取: %s - %s (%v)", item.Artist, item.Name, err)
		} else {
			lg("歌词完成: %s", lrcPath)
		}
	}

	// 4) 封面：同目录 cover.jpg（同目录只下一次）
	if containsTypes(types, "cover") && strings.TrimSpace(item.CoverURL) != "" {
		coverPath := filepath.Join(filepath.Dir(audioPath), "cover.jpg")
		coverMu.Lock()
		need := !coverDone[coverPath]
		if need {
			coverDone[coverPath] = true
		}
		coverMu.Unlock()
		if need {
			if st, statErr := os.Stat(coverPath); statErr == nil && st.Size() > 0 && !cfg.DownloadOverwrite {
				lg("封面已存在，跳过: %s", coverPath)
			} else if err := s.fetchCover(ctx, client, cfg, item.CoverURL, coverPath); err != nil {
				lg("封面未获取: %v", err)
			}
		}
	}

	return dlOK
}

// ---------------------------------------------------------------------------
// go-music-api 交互
// ---------------------------------------------------------------------------

// apiURL 拼接 go-music-api 地址（baseURL 已在 validateConfig 中强制为回环）。
func apiURL(base, path string, q url.Values) string {
	u := strings.TrimSuffix(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// retryableError 标记「可重试」的失败。
//
// 背景：音乐平台对直链签发有限流/风控。实测同一首歌 id 连续请求会时好时坏
// （同一 id 连续 3 次：1 次 200、2 次返回 "download url not found (might be vip or copyright restricted)"），
// 因此必须重试，否则用户会看到大量莫名其妙的失败。
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }

// fetchStream 通过 go-music-api 的 stream 代理下载音频（由其处理各平台防盗链），
// 带指数退避重试。先写 .part 再 rename，避免半截文件被 Navidrome 扫描入库。
func (s *Server) fetchStream(ctx context.Context, client *http.Client, cfg Config,
	item DownloadItem, dst string, lg func(string, ...any)) error {

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.fetchStreamOnce(ctx, client, cfg, item, dst)
		if err == nil {
			return nil
		}
		lastErr = err
		var re retryableError
		if !errors.As(err, &re) {
			return err // 不可重试的错误（如路径无效）直接返回
		}
		if attempt < maxAttempts {
			wait := time.Duration(attempt*attempt) * time.Second // 1s、4s
			lg("第 %d 次下载失败（%v），%v 后重试", attempt, err, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return fmt.Errorf("已重试 %d 次仍失败: %w", maxAttempts, lastErr)
}

// fetchStreamOnce 单次尝试下载音频。
func (s *Server) fetchStreamOnce(ctx context.Context, client *http.Client, cfg Config, item DownloadItem, dst string) error {
	q := url.Values{}
	q.Set("id", item.ID)
	q.Set("source", item.Source)
	q.Set("name", item.Name)
	q.Set("artist", item.Artist)
	if item.Duration > 0 {
		q.Set("duration", fmt.Sprintf("%d", item.Duration))
	}
	endpoint := apiURL(cfg.MusicApiBaseURL, "/api/v1/music/stream", q)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return retryableError{err} // 网络类错误可重试
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpErr := fmt.Errorf("接口返回 %d", resp.StatusCode)
		// 404 与 5xx 多由平台限流引起，可重试；其余（如 400 参数错）直接失败
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode >= 500 {
			return retryableError{httpErr}
		}
		return httpErr
	}

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return retryableError{fmt.Errorf("写入失败: %w", err)}
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if n < minAudioBytes {
		_ = os.Remove(tmp)
		return retryableError{fmt.Errorf("内容过小（%d 字节），疑似非音频", n)}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	chownToNavidrome(dst)
	return nil
}

// apiDataGet 调用 go-music-api 的 JSON 接口并返回 data 字段。
func (s *Server) apiDataGet(ctx context.Context, client *http.Client, cfg Config, path string, q url.Values, out any) error {
	endpoint := apiURL(cfg.MusicApiBaseURL, path, q)
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.MusicApiTimeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 上限 8MB，防异常大响应
	if err != nil {
		return err
	}
	var wrapper struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return fmt.Errorf("响应解析失败: %w", err)
	}
	if wrapper.Code != 200 {
		return fmt.Errorf("接口错误 %d: %s", wrapper.Code, wrapper.Msg)
	}
	if len(wrapper.Data) == 0 {
		return errors.New("接口未返回数据")
	}
	return json.Unmarshal(wrapper.Data, out)
}

// fetchLyric 拉取歌词并写入 .lrc（空歌词视为失败，不落文件）。
func (s *Server) fetchLyric(ctx context.Context, client *http.Client, cfg Config, item DownloadItem, dst string) error {
	q := url.Values{}
	q.Set("id", item.ID)
	q.Set("source", item.Source)
	var data struct {
		Lyric string `json:"lyric"`
	}
	if err := s.apiDataGet(ctx, client, cfg, "/api/v1/music/lyric", q, &data); err != nil {
		return err
	}
	text := strings.TrimSpace(data.Lyric)
	if text == "" || text == "No lyrics found" {
		return errors.New("无歌词")
	}
	if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
		return err
	}
	chownToNavidrome(dst)
	return nil
}

// fetchCover 通过 go-music-api 的封面代理下载图片。
func (s *Server) fetchCover(ctx context.Context, client *http.Client, cfg Config, coverURL, dst string) error {
	q := url.Values{}
	q.Set("url", coverURL)
	endpoint := apiURL(cfg.MusicApiBaseURL, "/api/v1/music/cover", q)

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("封面接口返回 %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return err
	}
	if len(b) < 512 {
		return errors.New("封面内容过小")
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	chownToNavidrome(dst)
	return nil
}

// chownToNavidrome 把单个文件/目录的属主改为 navidrome:navidrome。
// 助手以 root 运行，落盘文件默认属 root；统一归属后 Navidrome 才能正常维护库文件。
// 失败仅记日志：不影响读取与播放。
func chownToNavidrome(path string) {
	uid, gid, ok := navidromeIDs()
	if !ok {
		return
	}
	if err := os.Chown(path, uid, gid); err != nil {
		log.Printf("chown 失败（忽略）: %s (%v)", path, err)
	}
}

// navidromeIDs 查询 navidrome 用户的 uid/gid（与 files.go 的 chownTree 保持同一策略）。
func navidromeIDs() (int, int, bool) {
	usr, err := user.Lookup("navidrome")
	if err != nil {
		return 0, 0, false
	}
	uid, err := strconv.Atoi(usr.Uid)
	if err != nil {
		return 0, 0, false
	}
	gid, err := strconv.Atoi(usr.Gid)
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}
