package main

import (
	"testing"
)

// 真实 baidupcs tree 输出片段（含目录、深层文件、中文名、非音乐目录）。
const treeSample = `[ undefined]
├── Acro Jazz Laboratories/
│   ├── acro jazz one/
│   │   ├── Realize feat Sam Ock - Acro Jazz Laboratories.lrc
│   │   └── Realize feat Sam Ock - Acro Jazz Laboratories.mp3
├── attachments/
│   ├── 01/
│   │   ├── d9/
│   │   │   └── cover.jpg
├── 白水寒/
│   └── 白水寒 - 张三.mp3
└── 最后一次分离 - 情词尧.mp3
`

func TestParseRemotePathsRebuildsFullPaths(t *testing.T) {
	root := "/apps/Cloud Sync/music"
	got := parseRemotePaths(treeSample, root)

	want := map[string]bool{
		root + "/Acro Jazz Laboratories/acro jazz one/Realize feat Sam Ock - Acro Jazz Laboratories.lrc": true,
		root + "/Acro Jazz Laboratories/acro jazz one/Realize feat Sam Ock - Acro Jazz Laboratories.mp3": true,
		root + "/attachments/01/d9/cover.jpg": true,
		root + "/白水寒/白水寒 - 张三.mp3":            true,
		root + "/最后一次分离 - 情词尧.mp3":            true,
	}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 个文件，实际 %d 个: %v", len(want), len(got), got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("多出或路径错误的条目: %q", p)
		}
	}
}

func TestFilterOutDirsSkipsExcluded(t *testing.T) {
	root := "/apps/Cloud Sync/music"
	paths := parseRemotePaths(treeSample, root)
	cfg := Config{RemoteRoot: root, ExcludeDirs: []string{"attachments"}}

	kept, skipped := filterOutDirs(paths, cfg)
	if skipped != 1 {
		t.Errorf("应排除 1 个文件（attachments 下的 cover.jpg），实际 %d", skipped)
	}
	for _, p := range kept {
		if p == root+"/attachments/01/d9/cover.jpg" {
			t.Errorf("attachments 下的文件不应保留: %q", p)
		}
	}
	if len(kept) != len(paths)-1 {
		t.Errorf("保留数量应为 %d，实际 %d", len(paths)-1, len(kept))
	}
}

func TestFilterByTypesKeepsOnlyAllowedExtensions(t *testing.T) {
	root := "/apps/Cloud Sync/music"
	paths := []string{
		root + "/a.mp3", root + "/a.lrc", root + "/cover.jpg", root + "/readme.txt", root + "/video.mp4",
	}
	cfg := Config{RemoteRoot: root, IncludeLyrics: true, IncludeCovers: true}
	got := filterByTypes(paths, cfg, defaultTypes(cfg))

	allowed := map[string]bool{root + "/a.mp3": true, root + "/a.lrc": true, root + "/cover.jpg": true}
	if len(got) != len(allowed) {
		t.Fatalf("期望 %d 个，实际 %d: %v", len(allowed), len(got), got)
	}
	for _, p := range got {
		if !allowed[p] {
			t.Errorf("不应包含 %q（非歌曲/歌词/封面）", p)
		}
	}
}

func TestCountLsEntries(t *testing.T) {
	out := `[ undefined]

当前目录: /apps/Cloud Sync/music
----
   #     文件大小         修改日期                                 文件(目录)
    0             -  2025-09-15 02:45:47  Acro Jazz Laboratories/
    1             -  2025-09-15 02:45:47  Akia, K'ream/
`
	if n := countLsEntries(out); n != 2 {
		t.Errorf("应识别 2 条，实际 %d", n)
	}
}
