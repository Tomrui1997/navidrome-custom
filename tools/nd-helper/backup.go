package main

// 下载完成后的「网盘备份」阶段（防丢失）。
//
// 设计要点：
//  1. 备份目录与本地下载目录一一对应（RemoteRoot + 本地目录名，如 /apps/Cloud Sync/music/在线音乐），
//     本地文件丢失时可由现有「网盘→本地」同步原样拉回，天然形成恢复通道。
//  2. 同名文件交给 baidupcs 跳过（默认策略 skip），重复执行幂等。
//  3. 单文件失败不阻断整体；进度写入任务快照（backup 字段）与按天日志。
//  4. 远端路径做包含性校验（remoteWithin），防止拼接越界写到备份目录之外。

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// backupTarget 一首成功处理歌曲的待备份文件（本地绝对路径；空串 = 该文件不存在）。
type backupTarget struct {
	dir   string // 歌曲所在本地目录（用于计算远端目标目录；即使个别文件缺失也可用）
	audio string
	lrc   string
	cover string
}

// backupFile 单个待备份文件（kind 仅用于日志可读性）。
type backupFile struct {
	path string
	kind string
}

// backupRemoteRoot 计算网盘备份根目录：RemoteRoot + 本地下载目录名。
// 例：RemoteRoot=/apps/Cloud Sync/music、DownloadDir=/opt/navidrome/music/在线音乐
// → /apps/Cloud Sync/music/在线音乐。
// 实现用 path（纯 / 语义）而不依赖 filepath.Base 的平台差异，保证单测跨平台一致。
func backupRemoteRoot(cfg Config) string {
	local := strings.TrimSuffix(filepath.ToSlash(cfg.DownloadDir), "/")
	return strings.TrimSuffix(cfg.RemoteRoot, "/") + "/" + path.Base(local)
}

// remoteWithin 校验 child 位于 root 之内（先 Clean 再前缀比较，防 .. 穿越）。
func remoteWithin(root, child string) error {
	r := path.Clean(filepath.ToSlash(root))
	c := path.Clean(filepath.ToSlash(child))
	if c == r || strings.HasPrefix(c, r+"/") {
		return nil
	}
	return fmt.Errorf("目标路径 %q 不在 %q 之内", child, root)
}

// isUploadSkipped 判断 baidupcs upload 的输出是否为「已存在，跳过」。
// 生产实测样例：[1] 目标文件, /apps/Cloud Sync/music/在线音乐/阿杜/阿杜 - Andy.lrc, 已存在, 跳过...
func isUploadSkipped(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "已存在") && strings.Contains(s, "跳过")
}

// uploadFile 备份单个文件，返回 (成功, 跳过, 错误)。
func (s *Server) uploadFile(fpath, remoteDir string) (bool, bool, error) {
	out, err := s.baidupcs("upload", fpath, remoteDir)
	if err != nil {
		return false, false, err
	}
	if isUploadSkipped(out) {
		return false, true, nil
	}
	return true, false, nil
}

// backupToNetdisk 把本轮成功歌曲的音频/歌词/封面上传到网盘（逐首；同名跳过）。
func (s *Server) backupToNetdisk(ctx context.Context, task *DownloadTask, cfg Config, lg func(string, ...any)) {
	task.mu.Lock()
	targets := make([]backupTarget, len(task.backupTargets))
	copy(targets, task.backupTargets)
	task.backupTotal = len(targets)
	task.mu.Unlock()
	if len(targets) == 0 {
		return
	}

	root := backupRemoteRoot(cfg)
	if err := remoteWithin(strings.TrimSuffix(cfg.RemoteRoot, "/"), root); err != nil {
		lg("备份跳过: %v", err)
		return
	}
	lg("开始备份到网盘: %s（共 %d 首）", root, len(targets))
	s.audit("backup_start root=%q total=%d", root, len(targets))

	madeDirs := map[string]bool{}  // 远端目录创建去重
	coverSent := map[string]bool{} // 封面按目录去重（同一目录多首共用一个 cover.jpg）

	for _, tgt := range targets {
		if ctx.Err() != nil {
			lg("备份中止（任务已取消）")
			break
		}
		rel, err := filepath.Rel(cfg.DownloadDir, tgt.dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			task.incBackupFailed()
			lg("备份失败（路径越界）: %s", tgt.dir)
			continue
		}
		remoteDir := root
		if rel != "." {
			remoteDir = root + "/" + filepath.ToSlash(rel)
		}
		if err := remoteWithin(root, remoteDir); err != nil {
			task.incBackupFailed()
			lg("备份失败: %v", err)
			continue
		}
		if !madeDirs[remoteDir] {
			// 目录创建（幂等；失败交由 upload 报真实错误）
			s.baidupcsMkdir(remoteDir)
			madeDirs[remoteDir] = true
		}

		// 只备份实际存在的文件（如仅下载了歌词、或封面缺失等边界场景）
		var files []backupFile
		if tgt.audio != "" {
			files = append(files, backupFile{path: tgt.audio, kind: "音频"})
		}
		if tgt.lrc != "" {
			files = append(files, backupFile{path: tgt.lrc, kind: "歌词"})
		}
		if tgt.cover != "" && !coverSent[remoteDir] {
			files = append(files, backupFile{path: tgt.cover, kind: "封面"})
			coverSent[remoteDir] = true
		}
		if len(files) == 0 {
			continue
		}

		done, skipped, failed := 0, 0, 0
		for _, f := range files {
			if ctx.Err() != nil {
				break
			}
			task.setBackupCurrent(path.Base(f.path))
			ok, skip, uerr := s.uploadFile(f.path, remoteDir)
			switch {
			case uerr != nil:
				failed++
				task.incBackupFailed()
				lg("备份失败（%s）: %s（%v）", f.kind, filepath.Base(f.path), uerr)
			case skip:
				skipped++
				task.incBackupSkip()
			case ok:
				done++
				task.incBackupDone()
			}
		}
		lg("备份完成: %s（成功 %d，跳过 %d，失败 %d）", filepath.Base(files[0].path), done, skipped, failed)
	}
	task.setBackupCurrent("")
	task.mu.Lock()
	done, skipped, failed := task.backupDone, task.backupSkipped, task.backupFailed
	task.mu.Unlock()
	lg("备份结束：成功 %d，跳过 %d，失败 %d", done, skipped, failed)
	s.audit("backup_done ok=%d skipped=%d failed=%d", done, skipped, failed)
}
