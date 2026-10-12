package main

import "testing"

// 备份根目录 = RemoteRoot + 本地下载目录名（保证与「网盘→本地」同步一一对应）。
func TestBackupRemoteRoot(t *testing.T) {
	cases := []struct {
		remote   string
		download string
		want     string
	}{
		{"/apps/Cloud Sync/music", "/opt/navidrome/music/在线音乐", "/apps/Cloud Sync/music/在线音乐"},
		{"/apps/Cloud Sync/music/", "/opt/navidrome/music/在线音乐/", "/apps/Cloud Sync/music/在线音乐"},
		{"/r", "/d/在线音乐", "/r/在线音乐"},
	}
	for _, c := range cases {
		got := backupRemoteRoot(Config{RemoteRoot: c.remote, DownloadDir: c.download})
		if got != c.want {
			t.Errorf("backupRemoteRoot(%q,%q) = %q, 期望 %q", c.remote, c.download, got, c.want)
		}
	}
}

// 备份路径必须被限制在备份根之内（先 Clean 再比较，防 .. 穿越与相似前缀误放行）。
func TestRemoteWithin(t *testing.T) {
	root := "/apps/Cloud Sync/music/在线音乐"
	ok := []string{
		root,
		root + "/阿杜",
		root + "/阿杜/子目录",
	}
	bad := []string{
		"/apps/Cloud Sync/music",
		"/apps/Cloud Sync/music/在线音乐2",
		"/apps/Cloud Sync/music/在线音乐/../abc",
		"/other",
	}
	for _, p := range ok {
		if err := remoteWithin(root, p); err != nil {
			t.Errorf("remoteWithin(%q) 应通过: %v", p, err)
		}
	}
	for _, p := range bad {
		if err := remoteWithin(root, p); err == nil {
			t.Errorf("remoteWithin(%q) 应被拒绝", p)
		}
	}
}

// baidupcs upload 输出解析：已存在跳过（样例来自生产实测），成功输出不得误判为跳过。
func TestIsUploadSkipped(t *testing.T) {
	skip := []byte("[1] 目标文件, /apps/Cloud Sync/music/在线音乐/阿杜/阿杜 - Andy.lrc, 已存在, 跳过...\n上传结束, 时间: 219ms, 总大小: 0B")
	if !isUploadSkipped(skip) {
		t.Error("已存在跳过的输出应被识别")
	}
	okOut := []byte("[1] 上传文件成功, 保存到网盘路径: /apps/Cloud Sync/music/在线音乐/阿杜/阿杜 - Andy.lrc")
	if isUploadSkipped(okOut) {
		t.Error("成功输出不应被误判为跳过")
	}
	if isUploadSkipped([]byte("")) {
		t.Error("空输出不应被误判为跳过")
	}
}
