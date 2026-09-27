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
