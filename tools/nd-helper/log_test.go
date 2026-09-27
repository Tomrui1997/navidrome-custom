package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- 日志过滤（控制日志体积的关键逻辑）----

func TestFilterLogLineDropsNoise(t *testing.T) {
	noise := []string{
		"  文件名称 踏浪 - 徐怀钰.lrc",
		"  文件大小 1239, 1.209961KB",
		"  md5 (截图请打码) 2e11b63642d374ade53904e76c72f3f0",
		"  app_id 0",
		"  fs_id 629279591943877",
		"  创建日期 2025-09-15 02:47:07",
		"  修改日期 2025-09-15 02:47:07",
		"  类型              文件",
		"----",
		"",
		"[ undefined]",
	}
	for _, line := range noise {
		if got := filterLogLine(line); got != "" {
			t.Errorf("噪音行应被丢弃，实际保留为 %q（输入 %q）", got, line)
		}
	}
}

func TestFilterLogLineKeepsProgressAndCompressesURL(t *testing.T) {
	keep := []string{
		"[438] 准备下载: /apps/Cloud Sync/music/踏浪 - 徐怀钰.lrc",
		"[438] 下载完成, 保存位置: /opt/navidrome/music/踏浪 - 徐怀钰.lrc",
		"[438] 文件已经存在: /opt/navidrome/music/x.mp3, 跳过...",
		"范围：远端共 430 个文件，排除目录跳过 5 个，符合歌曲/歌词/封面要求的 425 个",
	}
	for _, line := range keep {
		if got := filterLogLine(line); got != line {
			t.Errorf("有效行不应被改动：\n 输入 %q\n 输出 %q", line, got)
		}
	}
	// 失败行里的超长直链要被压缩，但错误原因必须保留（判定成败依赖它）
	raw := `[753] 下载文件失败, Get "https://bjdd-ct11.baidupcs.com/file/abc?sign=xxx&fid=1": dial tcp [240e::1]:443: connect: cannot assign requested address, 重试 3/3`
	got := filterLogLine(raw)
	if strings.Contains(got, "https://") {
		t.Errorf("直链应被压缩为 <url>，实际：%q", got)
	}
	if !strings.Contains(got, "cannot assign requested address") {
		t.Errorf("错误原因必须保留（用于判定失败），实际：%q", got)
	}
	if len(got) > 600 {
		t.Errorf("单行应被截断到 600 字符内，实际 %d", len(got))
	}
}

func TestPreflightAndErrorMarkersCoverSilentFailures(t *testing.T) {
	// baidupcs 对不存在的目录会以退出码 0 结束，只靠退出码会把失败当成成功
	sample := "获取目录下的文件列表: 遇到错误, 远端服务器返回错误, 代码: -9, 消息: 文件不存在"
	hit := false
	for _, p := range remoteErrorMarkers {
		if strings.Contains(sample, p) {
			hit = true
			break
		}
	}
	if !hit {
		t.Error("remoteErrorMarkers 必须能识别「目录不存在」这类静默失败输出")
	}
	for _, p := range []string{"获取目录下的文件列表", "user is not authorized", "network is unreachable"} {
		found := false
		for _, f := range fatalLogPatterns {
			if f == p {
				found = true
			}
		}
		if p == "network is unreachable" {
			for _, f := range transientNetPatterns {
				if f == p {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("致命/网络错误模式缺少 %q", p)
		}
	}
}

// ---- 日志按天分文件 + 倒读尾部 ----

func TestSyncLogPathIsPerDay(t *testing.T) {
	d := time.Date(2026, 9, 27, 3, 0, 0, 0, time.Local)
	got := syncLogPathFor(d)
	if !strings.HasSuffix(got, "2026-09-27.log") {
		t.Errorf("日志应按天分文件，实际 %q", got)
	}
	// 用 filepath 自己比较，避免 Windows 分隔符差异
	if filepath.Dir(got) != filepath.Clean(syncLogDir) {
		t.Errorf("日志应位于 %q，实际 %q", filepath.Clean(syncLogDir), filepath.Dir(got))
	}
}

func TestReadTailLinesFromEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-27.log")
	var sb strings.Builder
	// 造 5000 行 × 约 100 字节 ≈ 500KB，确保超过 64KB 块大小以覆盖「跨块倒读」
	for i := 1; i <= 5000; i++ {
		sb.WriteString("line")
		sb.WriteString(strings.Repeat("x", 90))
		sb.WriteString("\n")
	}
	sb.WriteString("LAST-LINE\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, _ := readTailLines(path, 5)
	if len(lines) != 5 {
		t.Fatalf("应返回 5 行，实际 %d", len(lines))
	}
	if lines[len(lines)-1] != "LAST-LINE" {
		t.Errorf("最后一行应为 LAST-LINE，实际 %q", lines[len(lines)-1])
	}
	// 跨块倒读：请求 400 行（约 40KB）应正确取尾并标记还有更早内容
	lines2, truncated := readTailLines(path, 400)
	if len(lines2) != 400 {
		t.Fatalf("应返回 400 行，实际 %d", len(lines2))
	}
	if !truncated {
		t.Error("应标记还有更早内容未返回")
	}
}

func TestCountDoneSinceCountsUniquePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	content := "old\n" +
		"[1] 下载完成, 保存位置: /m/a.mp3\n" +
		"[1] 下载完成, 保存位置: /m/a.mp3\n" + // 重复（重试）只算一次
		"[2] 下载完成, 保存位置: /m/b.mp3\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	n := countDoneSince(path, int64(len("old\n")))
	if n != 2 {
		t.Errorf("去重后应统计 2 个文件，实际 %d", n)
	}
}

// 跳过行也要计入「已处理」：增量同步时本地已是最新，全靠跳过推进，
// 若不统计会出现进度长时间 0/N 的假卡死现象。
func TestCountProgressSinceCountsSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	content := "old\n" +
		"[1] 下载完成, 保存位置: /m/a.mp3\n" +
		"[1] 文件已经存在: /m/a.mp3, 跳过...\n" + // 同一文件先失败重试后成功，跳过行不应重复计数
		"[2] 文件已经存在: /m/b.lrc, 跳过...\n" +
		"[2] 文件已经存在: /m/b.lrc, 跳过...\n" + // 重复跳过只算一次
		"[3] 文件已经存在: /m/c.mp3, 跳过...\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	done, skipped := countProgressSince(path, int64(len("old\n")))
	if done != 1 {
		t.Errorf("应统计 1 个已下载文件，实际 %d", done)
	}
	if skipped != 3 {
		t.Errorf("应统计 3 个跳过文件（去重），实际 %d", skipped)
	}
}

// 阶段字段是「看起来卡死」的直接修复：必须有可读的阶段与阶段耗时。
func TestSnapshotExposesStageAndProcessed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(path, []byte("[1] 文件已经存在: /m/a.mp3, 跳过...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := &SyncTask{running: true, mode: "incremental", startedAt: time.Now(), logPath: path}
	task.setPlan(10, 0)
	task.setStage("download")
	task.setDirs(3)
	task.incDir()
	snap := task.snapshot()
	if snap["stage"] != "download" {
		t.Errorf("stage 应为 download，实际 %v", snap["stage"])
	}
	if snap["filesProcessed"] != 1 || snap["filesSkipped"] != 1 {
		t.Errorf("已处理/跳过计数异常: %v/%v", snap["filesProcessed"], snap["filesSkipped"])
	}
	if snap["dirsDone"] != 1 || snap["dirsTotal"] != 3 {
		t.Errorf("目录进度异常: %v/%v", snap["dirsDone"], snap["dirsTotal"])
	}
	if _, ok := snap["stageElapsed"]; !ok {
		t.Error("快照必须包含 stageElapsed")
	}
}
