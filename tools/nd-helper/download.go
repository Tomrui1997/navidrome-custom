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
	"os/exec"
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

// maxFailedItems 快照中最多回传多少条失败项（供前端「重试失败的 N 首」）。
// 单次任务上限为 200 首（maxDownloadItems），此上限足以覆盖全部失败项。
const maxFailedItems = 200

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
	// failedItems 失败项的**原始条目**（id/source/name/artist…）。
	// 只记文本的话页面既看不出「还剩多少首没下完」，也无法一键重试（真实反馈）。
	failedItems []DownloadItem

	// ------ 备份到网盘（防丢失）------
	backupTargets []backupTarget // 本轮成功处理、待备份的文件（音频 + 可选歌词/封面）
	backupTotal   int
	backupDone    int
	backupSkipped int
	backupFailed  int
	backupCurrent string
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

// ------ 备份到网盘：清单与进度（worker 并发调用，全部加锁）------

// addBackupTarget 记录一首成功歌曲待备份的文件。
func (t *DownloadTask) addBackupTarget(bt backupTarget) {
	t.mu.Lock()
	t.backupTargets = append(t.backupTargets, bt)
	t.mu.Unlock()
}

// incBackupDone / incBackupSkip / incBackupFailed 按「文件」粒度累加备份计数。
func (t *DownloadTask) incBackupDone() { t.mu.Lock(); t.backupDone++; t.mu.Unlock() }
func (t *DownloadTask) incBackupSkip() { t.mu.Lock(); t.backupSkipped++; t.mu.Unlock() }
func (t *DownloadTask) incBackupFailed() {
	t.mu.Lock()
	t.backupFailed++
	t.mu.Unlock()
}

// setBackupCurrent 设置正在备份的文件名（供快照展示）。
func (t *DownloadTask) setBackupCurrent(name string) {
	t.mu.Lock()
	t.backupCurrent = name
	t.mu.Unlock()
}

// addFailure 记录一个失败项：文本明细给页面展示，原始条目留给前端重试。
func (t *DownloadTask) addFailure(item DownloadItem, msg string) {
	t.mu.Lock()
	t.failed++
	if len(t.errs) < 20 {
		t.errs = append(t.errs, msg)
	}
	if len(t.failedItems) < maxFailedItems {
		t.failedItems = append(t.failedItems, item)
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
		// 失败项原始条目：前端据此显示「未完成数量」并提供一键重试
		"failedItems": t.failedItems,
		// 备份到网盘的进度（文件粒度；日志与排查用，前端未展示）
		"backup": map[string]any{
			"total":   t.backupTotal,
			"done":    t.backupDone,
			"skipped": t.backupSkipped,
			"failed":  t.backupFailed,
			"current": t.backupCurrent,
		},
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

// 不可见字符（零宽空格、方向标记、字节序标记、词连接符、蒙古文元音分隔符）。
// 为什么必须删除：strings.Fields 依据 unicode.IsSpace 判定空白，而这些字符不属于空白，
// 会被当作正文原样保留 → 目录名与标签里混入看不见的字符，用户搜「周杰伦」时匹配不到
// 形如「周杰<U+200B>伦」的记录，同一艺人的歌甚至会被分到两个文件夹。
var invisibleRe = regexp.MustCompile(`[\x{200b}\x{200e}\x{200f}\x{180e}\x{feff}\x{2060}]`)

// 非 ASCII 空白（不换行空格、全角空格、Ogham 空格、各种窄空格、行/段分隔符）。
// 为什么删除而不是转成普通空格：这类字符几乎只出现在脏数据里（常见于中文人名中间），
// 统一删除才能与正常写法合并；若转成空格反而会凭空造出「周杰 伦」这种新变体。
var unicodeSpaceRe = regexp.MustCompile("[\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]")

// normalizeField 归一化要「落盘成文件名 + 写进音频标签」的文本字段。
// 目录名、文件名、标签三处必须取自同一次归一化，否则同一首歌会因上游返回的隐形字符
// 而分裂成两个目录，或生成两条互相搜不到的记录。
func normalizeField(s string) string {
	s = invisibleRe.ReplaceAllString(s, "")
	s = unicodeSpaceRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// sanitizeName 清洗文件/目录名片段：归一化空白与隐形字符、剔除危险字符、去首尾点与空格、限长。
// 目的：即便歌曲名含 "../" 或 "/" 也无法逃逸出目标目录。
func sanitizeName(s string) string {
	s = normalizeField(s)
	s = unsafeNameRe.ReplaceAllString(s, "_")
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

// detectAudioFormat 通过文件头魔数嗅探**真实**音频容器。
//
// 为什么需要：平台声明的 ext 并不可信 —— 实测咪咕标称 SQ(FLAC) 却返回了 MP3 字节，
// 文件被命名为 .flac 后 Navidrome 的解析器选错而失败，元数据全丢：
// 标题退化成文件名、歌手/专辑变 [Unknown Artist]/[Unknown Album]，最终「按歌手搜不到」。
func detectAudioFormat(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	switch {
	case string(head[0:3]) == "ID3":
		return "mp3"
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		// 同步字均为 0xFFE；再靠 layer 位区分：ADTS AAC 的 layer 位为 00，MP3 为 01/10/11
		if head[1]&0x06 == 0 {
			return "aac"
		}
		return "mp3"
	case string(head[0:4]) == "fLaC":
		return "flac"
	case string(head[0:4]) == "OggS":
		return "ogg"
	case string(head[0:4]) == "RIFF" && len(head) >= 12 && string(head[8:12]) == "WAVE":
		return "wav"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return "m4a"
	case string(head[0:4]) == "MAC ":
		return "ape"
	case head[0] == 0x30 && head[1] == 0x26 && head[2] == 0xB2 && head[3] == 0x75:
		return "wma" // ASF 头
	}
	return ""
}

// sniffAudioExt 读取文件头返回真实扩展名（含点号）；识别不出或不在白名单时返回 ""。
func sniffAudioExt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	if n < 4 {
		return ""
	}
	return normalizeExt(detectAudioFormat(head[:n]))
}

// fixAudioExt 按实际内容纠正扩展名（与声明不符时改名），返回纠正后的路径与扩展名。
// 改名失败时原样返回，不阻断下载。
func fixAudioExt(path, curExt string, lg func(string, ...any)) (string, string) {
	real := sniffAudioExt(path)
	if real == "" || real == curExt {
		return path, curExt
	}
	// Ogg 容器可能是 Opus：保留原有的 .opus 命名，不降级成 .ogg
	if real == ".ogg" && curExt == ".opus" {
		return path, curExt
	}
	newPath := strings.TrimSuffix(path, curExt) + real
	if _, err := os.Stat(newPath); err == nil {
		// 正确命名的同名文件已存在 → 删掉这个命名错误的副本，避免库里出现两份
		if rmErr := os.Remove(path); rmErr == nil {
			lg("实际格式为 %s，已删除命名错误的重复文件: %s", real, filepath.Base(path))
		}
		return newPath, real
	}
	if err := os.Rename(path, newPath); err != nil {
		lg("扩展名纠正失败（保留原名）: %v", err)
		return path, curExt
	}
	lg("扩展名已按实际内容纠正: %s -> %s", filepath.Base(path), filepath.Base(newPath))
	return newPath, real
}

// ffmpegBin / ffprobeBin 定位媒体工具（优先 PATH，回退 /usr/local/bin）。
func ffmpegBin() string  { return lookBin("ffmpeg") }
func ffprobeBin() string { return lookBin("ffprobe") }

func lookBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	fallback := filepath.Join("/usr/local/bin", name)
	if st, err := os.Stat(fallback); err == nil && !st.IsDir() {
		return fallback
	}
	return ""
}

// audioTagState 音频文件的标签现状（供下载流程决定是否需要（重）写标签）。
type audioTagState struct {
	hasTitleArtist bool // title 与 artist 是否齐全
	hasLyrics      bool // 是否已有内嵌歌词（任意歌词键且内容非空）
}

// probeAudioTags 用 ffprobe 探测标签现状；探测失败返回零值（全 false）：
// 宁可多写一次标签（幂等），也不要漏写。
func probeAudioTags(ctx context.Context, path string) audioTagState {
	probe := ffprobeBin()
	if probe == "" {
		return audioTagState{}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, probe, "-v", "quiet",
		"-show_entries", "format_tags", "-of", "json", path).Output()
	if err != nil {
		return audioTagState{}
	}
	return parseAudioTagState(out)
}

// parseAudioTagState 解析 ffprobe 的 format_tags JSON（纯函数，便于单测）。
// 歌词键名各容器不一：MP3（TXXX）为 lyrics / lyrics:xxx / lyrics-eng，
// FLAC/OGG（Vorbis）为 LYRICS，M4A（iTunes）为 ©lyr（ffprobe 显示 lyrics）。
func parseAudioTagState(raw []byte) audioTagState {
	var res struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return audioTagState{}
	}
	state := audioTagState{}
	title, artist := "", ""
	for k, v := range res.Format.Tags {
		lk := strings.ToLower(k)
		switch {
		case lk == "title":
			title = v
		case lk == "artist":
			artist = v
		}
		if isLyricTagKey(lk) && strings.TrimSpace(v) != "" {
			state.hasLyrics = true
		}
	}
	state.hasTitleArtist = strings.TrimSpace(title) != "" && strings.TrimSpace(artist) != ""
	return state
}

// isLyricTagKey 判断 ffprobe 标签键是否为歌词（覆盖实测到的各种形态）。
// 注意不能用「包含 lyric」判定——lyricist（作词人）是另一个字段，会被误判。
func isLyricTagKey(lk string) bool {
	return lk == "lyrics" || lk == "unsynced_lyrics" || lk == "unsyncedlyrics" ||
		strings.HasPrefix(lk, "lyrics:") || strings.HasPrefix(lk, "lyrics-")
}

// lyricMetadataArgs 构造写入内嵌歌词的 ffmpeg -metadata 参数。
//
// 键名由生产实测锁定（Navidrome 的 TagLib 读取器只认这些形态）：
//   - MP3：TXXX「lyrics:xxx」（带语言码，TagLib 解析为 lyrics:xxx 键）；
//   - FLAC/OGG/M4A 等：Vorbis/iTunes 的「LYRICS」键。
//
// 为什么必须写内嵌歌词：Navidrome（本版本）与音流等客户端读取的是库内 lyrics 字段，
// 该字段仅来自文件内嵌歌词——磁盘上的 .lrc 侧车文件不会被采用（生产实测确认）。
func lyricMetadataArgs(ext, lyricText string) []string {
	if strings.TrimSpace(lyricText) == "" {
		return nil
	}
	if ext == ".mp3" {
		return []string{"-metadata", "lyrics:xxx=" + lyricText}
	}
	return []string{"-metadata", "LYRICS=" + lyricText}
}

// tagAudioFile 用 ffmpeg 把歌手/歌名/专辑与歌词写入音频标签（best-effort）。
//
// 为什么需要：平台返回的音频常常没有任何 ID3/Vorbis 标签，Navidrome 只能拿到标题（文件名），
// 歌手/专辑全部缺失 → 按歌手/专辑浏览与搜索都找不到。写入后即可正常检索。
// writeMeta 控制 title/artist/album 是否写入（对应 TagMetadata 开关）；
// lyricText 非空时写入内嵌歌词（独立于 writeMeta——勾选了歌词就应能显示出来）。
// 实现要点：先写临时文件再 rename，失败时原文件完好。
func tagAudioFile(ctx context.Context, path string, item DownloadItem, lyricText string, writeMeta bool, lg func(string, ...any)) error {
	bin := ffmpegBin()
	if bin == "" {
		return errors.New("未找到 ffmpeg")
	}
	ext := strings.ToLower(filepath.Ext(path))
	tmp := path + ".tagtmp" + ext
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", path,
		"-c", "copy", "-map", "0:a:0",
	}
	if writeMeta {
		args = append(args,
			"-metadata", "title="+item.Name,
			"-metadata", "artist="+item.Artist,
		)
		if strings.TrimSpace(item.Album) != "" {
			args = append(args, "-metadata", "album="+item.Album)
		}
	}
	args = append(args, lyricMetadataArgs(ext, lyricText)...)
	if ext == ".mp3" {
		// ID3v2.3 兼容性最好（部分老播放器/车机不认 2.4）
		args = append(args, "-id3v2_version", "3")
	}
	args = append(args, tmp)

	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(cctx, bin, args...).CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ffmpeg 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return errors.New("ffmpeg 输出为空，保留原文件")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换文件失败: %w", err)
	}
	lg("已写入元数据标签: %s", filepath.Base(path))
	return nil
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
		cancel:      cancel,
		running:     true,
		types:       types,
		total:       len(items),
		startedAt:   time.Now(),
		failedItems: make([]DownloadItem, 0, len(items)),
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
			res, tgt := s.downloadOne(ctx, client, cfg, item, types, lg, &coverMu, coverDone)
			switch res {
			case dlSkipped:
				task.incSkipped()
			case dlOK:
				task.incDone()
				if tgt != nil {
					task.addBackupTarget(*tgt)
				}
			case dlFailed:
				task.addFailure(item, item.Artist+" - "+item.Name)
			}
		}(it)
	}
	wg.Wait()

	// 统一修正属主（助手以 root 运行，落盘文件需归 navidrome，避免后续库操作受限）
	chownTree(cfg.DownloadDir)

	// 备份到网盘（防丢失）：把本轮成功歌曲的音频/歌词/封面上传到「RemoteRoot/本地目录名/…」。
	// 默认开启（配置 backupToNetdisk=false 可关）；同名文件由 baidupcs 跳过，重复执行幂等。
	if cfg.BackupToNetdisk {
		s.backupToNetdisk(ctx, task, cfg, lg)
	}

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
// 成功时返回待备份文件清单（供下载完成后的网盘备份阶段使用；失败返回 nil）。
func (s *Server) downloadOne(ctx context.Context, client *http.Client, cfg Config,
	item DownloadItem, types []string, lg func(string, ...any),
	coverMu *sync.Mutex, coverDone map[string]bool) (dlResult, *backupTarget) {

	// 0) 归一化三个文本字段。目录名、文件名、写入的标签必须都取自这份归一化结果，
	// 否则上游返回的隐形空白会让同一艺人的歌分裂到不同目录，或写出的标签搜不到。
	item.Name = normalizeField(item.Name)
	item.Artist = normalizeField(item.Artist)
	item.Album = normalizeField(item.Album)

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
		return dlFailed, nil
	}

	if err := os.MkdirAll(filepath.Dir(audioPath), 0o755); err != nil {
		lg("创建目录失败: %v", err)
		return dlFailed, nil
	}

	// 2) 音频（增量：已存在且非空且未开覆盖 → 跳过）
	downloaded := false
	if containsTypes(types, "audio") {
		if st, statErr := os.Stat(audioPath); statErr == nil && st.Size() > 0 && !cfg.DownloadOverwrite {
			lg("已存在，跳过: %s", audioPath)
			// 音频已存在时，仍然补一次歌词/封面（可能上次只下到音频）
		} else {
			if err := s.fetchStream(ctx, client, cfg, item, audioPath, lg); err != nil {
				lg("音频下载失败: %s - %s (%v)", item.Artist, item.Name, err)
				return dlFailed, nil
			}
			downloaded = true
			lg("音频完成: %s", audioPath)
		}
	}

	// 2.1) 校正扩展名（平台声明的 ext 可能与实际内容不符）。
	// 不做这步的后果（真实缺陷）：文件被错误命名后 Navidrome 解析失败，
	// 标题退化成文件名、歌手/专辑变 [Unknown Artist]/[Unknown Album] → 按歌手搜不到。
	if containsTypes(types, "audio") {
		if st, statErr := os.Stat(audioPath); statErr == nil && st.Size() > 0 {
			audioPath, ext = fixAudioExt(audioPath, ext, lg)
		}
	}

	// 2.2) 歌词：下载/读取同名 .lrc，并拿到歌词文本（下一步写入内嵌标签）。
	// 顺序调整原因：内嵌歌词要与元数据在同一次 ffmpeg 调用中写入，减少一次文件重写。
	lyricText := ""
	lrcPath := "" // 提升作用域：结尾构建备份清单时要用
	if containsTypes(types, "lyric") {
		lrcPath = strings.TrimSuffix(audioPath, ext) + ".lrc"
		if st, statErr := os.Stat(lrcPath); statErr == nil && st.Size() > 0 && !cfg.DownloadOverwrite {
			// 已存在：读取内容，供「文件缺内嵌歌词时补写」使用（如历史下载的文件）
			if b, rerr := os.ReadFile(lrcPath); rerr == nil {
				lyricText = strings.TrimPrefix(string(b), "\ufeff")
			}
			lg("歌词已存在，跳过: %s", lrcPath)
		} else if text, err := s.fetchLyric(ctx, client, cfg, item, lrcPath); err != nil {
			// 歌词失败不算整首失败（很多老歌本来就没有歌词）
			lg("歌词未获取: %s - %s (%v)", item.Artist, item.Name, err)
		} else {
			lyricText = text
			lg("歌词完成: %s", lrcPath)
		}
	}

	// 2.3) 写入音频标签：歌手/歌名/专辑（可选）+ 内嵌歌词（有歌词就写）。
	// 为什么歌词必须写内嵌：Navidrome 与音流等客户端读取的是库内 lyrics 字段，
	// 而该字段只来自文件内嵌歌词（.lrc 侧车不会被采用）——不写则歌词永远不显示。
	// 写入条件：刚下载、缺 title/artist、或有歌词但文件还没有内嵌歌词（补写场景）。
	if st, statErr := os.Stat(audioPath); statErr == nil && st.Size() > 0 {
		state := probeAudioTags(ctx, audioPath)
		needMeta := cfg.TagMetadata && (downloaded || !state.hasTitleArtist)
		needLyric := lyricText != "" && !state.hasLyrics
		if needMeta || needLyric {
			if err := tagAudioFile(ctx, audioPath, item, lyricText, cfg.TagMetadata, lg); err != nil {
				lg("元数据写入跳过: %s（%v）", filepath.Base(audioPath), err)
			}
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

	// 构建待备份清单：所有文件都「存在才带」（备份以防丢失为目的，宁可多传）。
	// 封面独立于本次 types 判断——目录里已有 cover.jpg（历史下载）也应备份；
	// 音频可能不存在（只勾了歌词）——此时仅备份实际存在的文件。
	tgt := &backupTarget{dir: filepath.Dir(audioPath)}
	if st, statErr := os.Stat(audioPath); statErr == nil && st.Size() > 0 {
		tgt.audio = audioPath
	}
	if lrcPath != "" {
		if st, statErr := os.Stat(lrcPath); statErr == nil && st.Size() > 0 {
			tgt.lrc = lrcPath
		}
	}
	coverPath := filepath.Join(filepath.Dir(audioPath), "cover.jpg")
	if st, statErr := os.Stat(coverPath); statErr == nil && st.Size() > 0 {
		tgt.cover = coverPath
	}
	if tgt.audio == "" && tgt.lrc == "" {
		return dlOK, nil // 没有任何可备份内容
	}
	return dlOK, tgt
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

// fetchLyric 拉取歌词：写入 .lrc（空歌词视为失败，不落文件），并返回歌词文本
// （供 downloadOne 写入音频内嵌标签）。
func (s *Server) fetchLyric(ctx context.Context, client *http.Client, cfg Config, item DownloadItem, dst string) (string, error) {
	q := url.Values{}
	q.Set("id", item.ID)
	q.Set("source", item.Source)
	var data struct {
		Lyric string `json:"lyric"`
	}
	if err := s.apiDataGet(ctx, client, cfg, "/api/v1/music/lyric", q, &data); err != nil {
		return "", err
	}
	text := strings.TrimSpace(data.Lyric)
	if text == "" || text == "No lyrics found" {
		return "", errors.New("无歌词")
	}
	if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
		return "", err
	}
	chownToNavidrome(dst)
	return text, nil
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
