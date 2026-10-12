package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// 命名清洗是防目录穿越的第一道防线，边界必须覆盖到。
func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"中文歌名原样保留", "周杰伦", "周杰伦"},
		{"中文带空格", "周杰伦 - 稻香", "周杰伦 - 稻香"},
		{"斜杠被替换（防穿越）", "a/b/c", "a_b_c"},
		{"反斜杠被替换", `a\b`, "a_b"},
		{"上跳路径被中和", "../../etc/passwd", "_.._etc_passwd"},
		{"Windows 保留字符", `a:b*c?d"e<f>g|h`, "a_b_c_d_e_f_g_h"},
		{"控制字符被替换", "a\x00b\x1fc", "a_b_c"},
		{"首尾点与空格被去掉", "  ..name..  ", "name"},
		{"空白折叠", "a    b", "a b"},
		{"全空白回退 unknown", "   ", "unknown"},
		{"空串回退 unknown", "", "unknown"},
		{"只有点号回退 unknown", "...", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeName(c.in); got != c.want {
				t.Errorf("sanitizeName(%q) = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 隐形空白是「同一艺人被拆成两个目录 / 库内搜不到」的直接原因，逐种字符都要覆盖。
func TestNormalizeFieldStripsInvisibleAndUnicodeSpaces(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"零宽空格被删除", "周杰\u200b伦", "周杰伦"},
		{"字节序标记被删除", "\ufeff周杰伦", "周杰伦"},
		{"方向标记被删除", "周杰\u200e伦", "周杰伦"},
		{"不换行空格被删除（与正常写法合并）", "周杰\u00a0伦", "周杰伦"},
		{"全角空格被删除（与正常写法合并）", "周杰\u3000伦", "周杰伦"},
		{"窄空格被删除", "周杰\u2009伦", "周杰伦"},
		{"普通连续空格折叠为单个", "周杰  伦", "周杰 伦"},
		{"制表符折叠为空格", "a\tb", "a b"},
		{"首尾空白被去掉", "  稻香  ", "稻香"},
		{"空串保持空串", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeField(c.in); got != c.want {
				t.Errorf("normalizeField(%q) = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 落盘命名必须继承归一化结果，否则隐形字符仍会进入目录名与文件名。
func TestSanitizeNameRemovesInvisibleChars(t *testing.T) {
	if got := sanitizeName("周杰\u200b伦"); got != "周杰伦" {
		t.Errorf("sanitizeName 未清除零宽空格: %q", got)
	}
	if got := sanitizeName("周杰\u3000伦 - 稻香"); got != "周杰伦 - 稻香" {
		t.Errorf("sanitizeName 未清除全角空格: %q", got)
	}
	// 普通空格是合法命名，必须保留（不能把「周杰 伦」误改成「周杰伦」）
	if got := sanitizeName("周杰 伦"); got != "周杰 伦" {
		t.Errorf("sanitizeName 误删了合法空格: %q", got)
	}
}

// 超长名称必须按 UTF-8 边界安全截断（中文名按字节截断会产生半个字符）。
func TestSanitizeNameTruncatesSafely(t *testing.T) {
	long := strings.Repeat("歌", 100) // 300 字节
	got := sanitizeName(long)
	if len(got) > maxNameLen {
		t.Errorf("截断后长度 %d 超过上限 %d", len(got), maxNameLen)
	}
	if !utf8.ValidString(got) {
		t.Errorf("截断产生了非法 UTF-8: %q", got)
	}
	// 120 字节 = 40 个汉字
	if want := strings.Repeat("歌", 40); got != want {
		t.Errorf("截断结果不符，得到 %d 个字符（期望 40）", utf8.RuneCountInString(got))
	}
}

func TestNormalizeExt(t *testing.T) {
	cases := map[string]string{
		"mp3":    ".mp3",
		".MP3":   ".mp3",
		" .flac": ".flac",
		".exe":   "",
		".txt":   "",
		"":       "",
		"lrc":    "",
	}
	for in, want := range cases {
		if got := normalizeExt(in); got != want {
			t.Errorf("normalizeExt(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestExtFromContentType(t *testing.T) {
	cases := map[string]string{
		"audio/mpeg":                ".mp3",
		"audio/mpeg; charset=utf-8": ".mp3",
		"audio/flac":                ".flac",
		"audio/x-flac":              ".flac",
		"AUDIO/MP4":                 ".m4a",
		"text/html":                 "",
		"application/json":          "",
		"":                          "",
	}
	for in, want := range cases {
		if got := extFromContentType(in); got != want {
			t.Errorf("extFromContentType(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// types 为空表示「全部下载」；显式列表则严格按列表判断。
func TestContainsTypes(t *testing.T) {
	if !containsTypes(nil, "audio") {
		t.Error("types 为空时应视为全部下载")
	}
	if containsTypes([]string{"lyric"}, "audio") {
		t.Error("types=[lyric] 时不应包含 audio")
	}
	if !containsTypes([]string{"audio", "lyric"}, "lyric") {
		t.Error("types=[audio,lyric] 应包含 lyric")
	}
}

// 关键词白名单：拼造出的异常 source 必须被拒绝。
func TestValidMusicSources(t *testing.T) {
	for _, ok := range []string{"netease", "qq", "kugou", "kuwo", "migu"} {
		if !validMusicSources[ok] {
			t.Errorf("%s 应在白名单内", ok)
		}
	}
	for _, bad := range []string{"", "unknown", "../../etc", "netease/../..", "HTTP://x"} {
		if validMusicSources[bad] {
			t.Errorf("%q 不应在白名单内", bad)
		}
	}
}

// 落点包含性校验：清洗后的路径必须仍落在下载目录内。
// 用平台无关的绝对路径，保证 Windows 与 Linux 都能跑。
func TestDownloadPathStaysWithinBase(t *testing.T) {
	base := filepath.Join(os.TempDir(), "在线音乐")
	item := DownloadItem{Artist: "../../../etc", Name: "../../passwd"}
	artistDir := filepath.Join(base, sanitizeName(item.Artist))
	stem := sanitizeName(item.Artist) + " - " + sanitizeName(item.Name)
	full := filepath.Join(artistDir, stem+".mp3")

	if _, err := ensureWithin(base, full); err != nil {
		t.Fatalf("清洗后仍越界（说明清洗规则失效）: %v", err)
	}
	if _, err := relTo(base, full); err != nil {
		t.Fatalf("relTo 判定越界: %v", err)
	}
	// 反向验证：未清洗的原始值确实会越界（证明这道防线是必要的）
	raw := filepath.Join(base, item.Artist, item.Name+".mp3")
	if _, err := relTo(base, raw); err == nil {
		t.Error("未清洗的路径应当被判为越界")
	}
}

// 配置校验：baseURL 必须回环（防 SSRF），并发与超时有界。
func TestValidateConfigMusicApiRules(t *testing.T) {
	// 路径类字段用平台绝对路径，保证 Windows / Linux 均可运行
	valid := func() Config {
		c := DefaultConfig()
		tmp := os.TempDir()
		c.RemoteRoot = filepath.Join(tmp, "remote")
		c.LocalRoot = filepath.Join(tmp, "local")
		c.BaidupcsBin = filepath.Join(tmp, "baidupcs")
		c.TrashDir = filepath.Join(tmp, "trash")
		c.DownloadDir = filepath.Join(tmp, "music", "在线音乐")
		return c
	}

	// 默认配置本身必须合法
	c := valid()
	if err := validateConfig(&c); err != nil {
		t.Fatalf("默认配置校验失败: %v", err)
	}
	if c.DownloadDir == "" || c.MusicApiBaseURL == "" {
		t.Fatal("默认配置缺少在线搜索字段")
	}

	// 非回环地址必须被拒绝
	for _, bad := range []string{
		"http://10.0.0.5:18080",
		"http://example.com:18080",
		"http://192.168.171.128:18080",
	} {
		c := valid()
		c.MusicApiBaseURL = bad
		if err := validateConfig(&c); err == nil {
			t.Errorf("baseURL=%q 应被拒绝（非回环）", bad)
		}
	}

	// 回环写法都应通过
	for _, ok := range []string{"http://127.0.0.1:18080", "http://localhost:18080"} {
		c := valid()
		c.MusicApiBaseURL = ok
		if err := validateConfig(&c); err != nil {
			t.Errorf("baseURL=%q 应被接受: %v", ok, err)
		}
	}

	// 并发与超时边界
	c = valid()
	c.DownloadConcurrency = 0
	if err := validateConfig(&c); err == nil {
		t.Error("downloadConcurrency=0 应被拒绝")
	}
	c = valid()
	c.MusicApiTimeoutSec = 1
	if err := validateConfig(&c); err == nil {
		t.Error("musicApiTimeoutSec=1 应被拒绝")
	}

	// 非法下载类型被过滤，且不允许为空
	c = valid()
	c.DownloadTypes = []string{"audio", "exe", "AUDIO", "lyric"}
	if err := validateConfig(&c); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(c.DownloadTypes) != 2 || c.DownloadTypes[0] != "audio" || c.DownloadTypes[1] != "lyric" {
		t.Errorf("下载类型规范化结果不符: %v", c.DownloadTypes)
	}
	c = valid()
	c.DownloadTypes = []string{"exe"}
	if err := validateConfig(&c); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(c.DownloadTypes) != 1 || c.DownloadTypes[0] != "audio" {
		t.Errorf("非法类型应回退为 audio，实际 %v", c.DownloadTypes)
	}
}

// 失败项必须保留**原始条目**：只记文本的话，页面既看不出「还剩多少首没下完」，
// 也无法一键重试（真实用户反馈驱动）。
func TestAddFailureKeepsOriginalItems(t *testing.T) {
	task := &DownloadTask{failedItems: make([]DownloadItem, 0, 4)}
	item := DownloadItem{ID: "abc123", Source: "kuwo", Name: "晴天", Artist: "周杰伦", Duration: 269}
	task.addFailure(item, item.Artist+" - "+item.Name)

	snap := task.snapshot()
	if snap["failed"] != 1 {
		t.Errorf("failed 计数应为 1，实际 %v", snap["failed"])
	}
	items, ok := snap["failedItems"].([]DownloadItem)
	if !ok {
		t.Fatalf("failedItems 类型异常: %#v", snap["failedItems"])
	}
	if len(items) != 1 {
		t.Fatalf("failedItems 应保留 1 条原始条目，实际 %d", len(items))
	}
	if items[0].ID != "abc123" || items[0].Source != "kuwo" || items[0].Name != "晴天" {
		t.Errorf("原始条目内容被破坏: %#v", items[0])
	}
	if errs, _ := snap["errors"].([]string); len(errs) != 1 {
		t.Errorf("errors 文本明细应为 1 条，实际 %v", snap["errors"])
	}
}

// failedItems 必须有上限保护，但 failed 计数不能被上限影响（计数必须真实）。
func TestAddFailureCapsItemsButKeepsCount(t *testing.T) {
	task := &DownloadTask{failedItems: make([]DownloadItem, 0, maxFailedItems)}
	over := maxFailedItems + 10
	for i := 0; i < over; i++ {
		task.addFailure(DownloadItem{ID: "same", Source: "kuwo"}, "失败")
	}
	if got := len(task.failedItems); got != maxFailedItems {
		t.Errorf("failedItems 应被限制为 %d，实际 %d", maxFailedItems, got)
	}
	if task.failed != over {
		t.Errorf("failed 计数应继续累加为 %d，实际 %d", over, task.failed)
	}
	if snap := task.snapshot(); snap["failed"] != over {
		t.Errorf("快照里的 failed 也应为 %d，实际 %v", over, snap["failed"])
	}
}

// 内容嗅探必须准确识别真实容器。
// 背景：平台会「标称 FLAC 实际返回 MP3」，按平台声明命名会让 Navidrome 解析失败 → 元数据全丢。
func TestDetectAudioFormat(t *testing.T) {
	head := func(b ...byte) []byte { return b }
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"ID3 开头的 mp3", head('I', 'D', '3', 0x03, 0x00), "mp3"},
		{"MPEG 帧同步（layer III）", head(0xFF, 0xFB, 0x90, 0x00), "mp3"},
		{"FLAC", head('f', 'L', 'a', 'C', 0x00, 0x00, 0x00, 0x22), "flac"},
		{"Ogg", head('O', 'g', 'g', 'S', 0x00, 0x02), "ogg"},
		{"WAV/RIFF", head('R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'A', 'V', 'E'), "wav"},
		{"M4A（ftyp）", head(0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'M', '4', 'A', ' '), "m4a"},
		{"APE", head('M', 'A', 'C', ' '), "ape"},
		{"ASF/WMA", head(0x30, 0x26, 0xB2, 0x75), "wma"},
		{"ADTS AAC（layer 位为 00）", head(0xFF, 0xF1, 0x50, 0x80), "aac"},
		{"无法识别", head(0x00, 0x01, 0x02, 0x03), ""},
		{"太短", head(0xFF, 0xFB), ""},
	}
	for _, c := range cases {
		if got := detectAudioFormat(c.in); got != c.want {
			t.Errorf("%s: detectAudioFormat=%q，期望 %q", c.name, got, c.want)
		}
	}
}

// 扩展名纠正：按实际内容改名，且不把 .opus 降级成 .ogg。
func TestFixAudioExtRenamesMislabeledFile(t *testing.T) {
	dir := t.TempDir()
	lg := func(string, ...any) {}

	// 内容其实是 MP3，却存成了 .flac
	mislabeled := filepath.Join(dir, "周传雄 - 侧脸.flac")
	if err := os.WriteFile(mislabeled, []byte("ID3\x03\x00\x00\x00rest-of-mp3-bytes"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	got, gotExt := fixAudioExt(mislabeled, ".flac", lg)
	want := filepath.Join(dir, "周传雄 - 侧脸.mp3")
	if got != want || gotExt != ".mp3" {
		t.Fatalf("纠正结果不符: path=%q ext=%q（期望 %q/.mp3）", got, gotExt, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("纠正后的文件不存在: %v", err)
	}
	if _, err := os.Stat(mislabeled); !os.IsNotExist(err) {
		t.Error("旧的错误命名文件应已被移除")
	}

	// 内容与扩展名一致 → 不动
	ok := filepath.Join(dir, "ok.flac")
	if err := os.WriteFile(ok, []byte("fLaC\x00\x00\x00\x22rest"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if p, e := fixAudioExt(ok, ".flac", lg); p != ok || e != ".flac" {
		t.Errorf("一致时不应改名: path=%q ext=%q", p, e)
	}

	// Ogg 容器保留 .opus 命名
	opus := filepath.Join(dir, "song.opus")
	if err := os.WriteFile(opus, []byte("OggS\x00\x02rest"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if p, e := fixAudioExt(opus, ".opus", lg); p != opus || e != ".opus" {
		t.Errorf(".opus 不应被降级为 .ogg: path=%q ext=%q", p, e)
	}

	// 目标文件已存在 → 删掉错误命名的副本
	dupFlac := filepath.Join(dir, "dup.flac")
	dupMp3 := filepath.Join(dir, "dup.mp3")
	_ = os.WriteFile(dupFlac, []byte("ID3\x03\x00\x00\x00wrong"), 0o644)
	_ = os.WriteFile(dupMp3, []byte("ID3\x03\x00\x00\x00right"), 0o644)
	if p, e := fixAudioExt(dupFlac, ".flac", lg); p != dupMp3 || e != ".mp3" {
		t.Errorf("重复时应保留正确命名的文件: path=%q ext=%q", p, e)
	}
	if b, err := os.ReadFile(dupMp3); err != nil || !strings.Contains(string(b), "right") {
		t.Error("正确命名的文件内容被破坏")
	}
}

// 内嵌歌词参数：MP3 必须用带语言码的 TXXX 键（lyrics:xxx），
// 其余容器用 LYRICS；空歌词不产生任何参数。
// 键名由生产实测锁定（Navidrome 的 TagLib 读取器只认这些形态）。
func TestLyricMetadataArgs(t *testing.T) {
	cases := []struct {
		name string
		ext  string
		text string
		want []string
	}{
		{"mp3 用 lyrics:xxx 键", ".mp3", "[00:01.00]abc", []string{"-metadata", "lyrics:xxx=[00:01.00]abc"}},
		{"flac 用 LYRICS 键", ".flac", "[00:01.00]abc", []string{"-metadata", "LYRICS=[00:01.00]abc"}},
		{"ogg 用 LYRICS 键", ".ogg", "abc", []string{"-metadata", "LYRICS=abc"}},
		{"m4a 用 LYRICS 键", ".m4a", "abc", []string{"-metadata", "LYRICS=abc"}},
		{"空歌词不添加参数", ".mp3", "", nil},
		{"纯空白歌词不添加参数", ".flac", "  \n\t ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lyricMetadataArgs(c.ext, c.text)
			if len(got) != len(c.want) {
				t.Fatalf("参数数量不符: got=%v want=%v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("参数[%d] = %q, 期望 %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// 标签探测解析：歌词键名各容器不一（lyrics / lyrics:xxx / lyrics-eng / LYRICS），
// 必须都能识别；lyricist（作词人）是另一字段，不能误判为歌词。
func TestParseAudioTagState(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantMeta   bool
		wantLyrics bool
	}{
		{"齐全+lyrics:eng", `{"format":{"tags":{"title":"T","artist":"A","lyrics:eng":"x"}}}`, true, true},
		{"齐全+lyrics-eng", `{"format":{"tags":{"title":"T","artist":"A","lyrics-eng":"x"}}}`, true, true},
		{"齐全+LYRICS", `{"format":{"tags":{"title":"T","artist":"A","LYRICS":"x"}}}`, true, true},
		{"齐全+unsynced_lyrics", `{"format":{"tags":{"title":"T","artist":"A","unsynced_lyrics":"x"}}}`, true, true},
		{"无歌词", `{"format":{"tags":{"title":"T","artist":"A"}}}`, true, false},
		{"lyricist 不算歌词", `{"format":{"tags":{"title":"T","artist":"A","lyricist":"W"}}}`, true, false},
		{"缺 artist", `{"format":{"tags":{"title":"T"}}}`, false, false},
		{"空对象", `{}`, false, false},
		{"非法 JSON", `not-json`, false, false},
		{"歌词为空串不算", `{"format":{"tags":{"title":"T","artist":"A","lyrics":""}}}`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseAudioTagState([]byte(c.raw))
			if got.hasTitleArtist != c.wantMeta || got.hasLyrics != c.wantLyrics {
				t.Errorf("parseAudioTagState(%s) = %+v, 期望 meta=%v lyrics=%v", c.raw, got, c.wantMeta, c.wantLyrics)
			}
		})
	}
}
