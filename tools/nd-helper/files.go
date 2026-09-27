package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// 请求/响应结构
// ---------------------------------------------------------------------------

// DeleteScope 指定删除范围：库记录 / 本地文件 / 网盘文件（可多选）。
type DeleteScope struct {
	Library bool `json:"library"`
	Local   bool `json:"local"`
	Remote  bool `json:"remote"`
}

// DeleteItem 一首待删除的曲目。
type DeleteItem struct {
	ID         string `json:"id"`
	LocalPath  string `json:"localPath"`
	RemotePath string `json:"remotePath"`
	Title      string `json:"title"`
}

// DeleteParams 删除接口参数。
type DeleteParams struct {
	Items     []DeleteItem `json:"items"`
	Scope     DeleteScope  `json:"scope"`
	Soft      bool         `json:"soft"`
	DryRun    bool         `json:"dryRun"`
	DeletedBy string       `json:"deletedBy"`
}

// DeleteResult 单个条目的处理结果。
type DeleteResult struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Local  string `json:"local"`  // ok | skipped | planned | failed
	Remote string `json:"remote"` // ok | skipped | planned | failed
	Reason string `json:"reason,omitempty"`
	// RemoteTarget 预演时回传的网盘目标路径，便于删除前核对（正式执行不返回）。
	RemoteTarget string `json:"remoteTarget,omitempty"`
}

// TrashEntry 回收站条目（本地与网盘各自的回收位置）。
type TrashEntry struct {
	ID            string    `json:"id"`
	ItemID        string    `json:"itemId"`
	Title         string    `json:"title"`
	RelPath       string    `json:"relPath"`
	LocalPath     string    `json:"localPath"`
	TrashedLocal  string    `json:"trashedLocal"`
	RemotePath    string    `json:"remotePath"`
	TrashedRemote string    `json:"trashedRemote"`
	Size          int64     `json:"size"`
	DeletedAt     time.Time `json:"deletedAt"`
	DeletedBy     string    `json:"deletedBy"`
}

// ---------------------------------------------------------------------------
// 路径安全校验
// ---------------------------------------------------------------------------

// isRemoteNotFound 判断 baidupcs 的报错是否属于「源文件不存在」。
// baidupcs 对不存在的路径返回形如：远端服务器返回错误, 代码: -9, 消息: 文件不存在
func isRemoteNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "文件不存在") ||
		strings.Contains(msg, "代码: -9") ||
		strings.Contains(msg, "file not found") ||
		strings.Contains(msg, "not exist")
}

// cleanAbs 返回规范化后的绝对路径。
func cleanAbs(p string) string {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
	}
	return filepath.Clean(p)
}

// ensureWithin 校验 child 是否位于 base 之内（防目录穿越）。
func ensureWithin(base, child string) (string, error) {
	b := cleanAbs(base)
	c := cleanAbs(child)
	if c == b {
		return c, nil
	}
	if !strings.HasPrefix(c, b+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径 %q 超出允许范围 %q", child, base)
	}
	return c, nil
}

// relTo 计算相对路径（用于映射到网盘路径），并做一次越界校验。
func relTo(base, child string) (string, error) {
	c, err := ensureWithin(base, child)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cleanAbs(base), c)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("相对路径越界")
	}
	return filepath.ToSlash(rel), nil
}

// remoteJoin 拼接网盘路径（POSIX 语义）。
func remoteJoin(base, rel string) string {
	return path.Join(strings.TrimSuffix(base, "/"), strings.TrimPrefix(rel, "/"))
}

// ---------------------------------------------------------------------------
// 回收站索引读写
// ---------------------------------------------------------------------------

func (s *Server) trashIndexPath() string {
	return filepath.Join(s.cfg.TrashDir, "index.json")
}

func (s *Server) readTrashIndex() []TrashEntry {
	b, err := os.ReadFile(s.trashIndexPath())
	if err != nil {
		return nil
	}
	var entries []TrashEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		log.Printf("回收站索引解析失败: %v", err)
		return nil
	}
	return entries
}

func (s *Server) writeTrashIndex(entries []TrashEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].DeletedAt.After(entries[j].DeletedAt) })
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.TrashDir, 0o755); err != nil {
		return err
	}
	return atomicWrite(s.trashIndexPath(), append(b, '\n'), 0o600)
}

// ---------------------------------------------------------------------------
// 删除
// ---------------------------------------------------------------------------

// actionDelete 执行（或预演）删除：本地文件与网盘文件的处理由助手负责；
// 数据库记录的删除由 Navidrome 应用侧完成（助手返回 itemID 供其使用）。
func (s *Server) actionDelete(p DeleteParams) (any, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	if !p.Scope.Library && !p.Scope.Local && !p.Scope.Remote {
		return nil, errors.New("删除范围不能为空")
	}
	// 安全兜底：配置要求软删时，即使调用方请求硬删也强制软删（防止误删不可恢复）
	if !p.Soft && cfg.SoftDelete && !p.DryRun {
		log.Printf("请求为硬删除，但配置 softDelete=true，已强制转为软删除")
		p.Soft = true
	}

	results := make([]DeleteResult, 0, len(p.Items))
	newEntries := []TrashEntry{}

	for _, item := range p.Items {
		r := DeleteResult{ID: item.ID, Title: item.Title, Local: "skipped", Remote: "skipped"}

		// ---- 本地文件 ----
		var rel string
		if item.LocalPath != "" {
			var err error
			rel, err = relTo(cfg.LocalRoot, item.LocalPath)
			if err != nil {
				r.Local = "failed"
				r.Reason = err.Error()
			}
		}
		if p.Scope.Local && r.Local != "failed" {
			if rel == "" {
				r.Local = "failed"
				r.Reason = "缺少本地路径"
			} else if _, err := os.Stat(item.LocalPath); err != nil {
				r.Local = "skipped"
				r.Reason = "本地文件不存在"
			} else if p.DryRun {
				r.Local = "planned"
			} else if p.Soft {
				dest := filepath.Join(cfg.TrashDir, "files", filepath.FromSlash(rel))
				// 必须在移动前取大小：移动后原路径已不存在，stat 会失败得到 0
				size := fileSize(item.LocalPath)
				if err := moveFile(item.LocalPath, dest); err != nil {
					r.Local = "failed"
					r.Reason = err.Error()
				} else {
					r.Local = "ok"
					newEntries = append(newEntries, TrashEntry{
						ID: newEntryID(), ItemID: item.ID, Title: item.Title, RelPath: rel,
						LocalPath: item.LocalPath, TrashedLocal: dest,
						DeletedAt: time.Now(), DeletedBy: p.DeletedBy, Size: size,
					})
				}
			} else {
				if err := os.Remove(item.LocalPath); err != nil {
					r.Local = "failed"
					r.Reason = err.Error()
				} else {
					r.Local = "ok"
				}
			}
		}

		// ---- 网盘文件 ----
		if p.Scope.Remote {
			if item.RemotePath == "" {
				r.Remote = "skipped"
			} else if _, err := ensureWithin(cfg.RemoteRoot, item.RemotePath); err != nil {
				r.Remote = "failed"
				if r.Reason == "" {
					r.Reason = err.Error()
				}
			} else if p.DryRun {
				r.Remote = "planned"
				r.RemoteTarget = item.RemotePath
			} else {
				var err error
				if p.Soft {
					remoteRel := rel
					if remoteRel == "" {
						remoteRel, err = relTo(cfg.RemoteRoot, item.RemotePath)
					}
					if err != nil {
						r.Remote = "failed"
					} else {
						dest := remoteJoin(cfg.RemoteTrashDir, remoteRel)
						if _, err = s.baidupcsMkdir(path.Dir(dest)); err == nil {
							_, err = s.baidupcs("mv", item.RemotePath, dest)
						}
						if err != nil {
							// 网盘侧源文件不存在（本地库里有、但网盘上已被移走/改名/早已删除）时，
							// 必须视为「已不存在」而不是失败：否则会连带阻塞库记录的删除，
							// 导致库里残留一堆指向不存在文件的记录（实测踩到过：59 条）。
							if isRemoteNotFound(err) {
								r.Remote = "skipped"
								if r.Reason == "" {
									r.Reason = "网盘上已不存在该文件（跳过）"
								}
							} else {
								r.Remote = "failed"
							}
						} else {
							r.Remote = "ok"
							// 若网盘侧先于本地记录了条目（仅网盘软删），补一条
							if len(newEntries) == 0 || newEntries[len(newEntries)-1].ItemID != item.ID {
								newEntries = append(newEntries, TrashEntry{
									ID: newEntryID(), ItemID: item.ID, Title: item.Title, RelPath: remoteRel,
									RemotePath: item.RemotePath, TrashedRemote: dest,
									DeletedAt: time.Now(), DeletedBy: p.DeletedBy,
								})
							} else {
								newEntries[len(newEntries)-1].RemotePath = item.RemotePath
								newEntries[len(newEntries)-1].TrashedRemote = dest
							}
						}
					}
				} else {
					if _, err = s.baidupcs("rm", item.RemotePath); err != nil {
						r.Remote = "failed"
					} else {
						r.Remote = "ok"
					}
				}
			}
		}

		// ---- 库记录 ----
		if p.Scope.Library {
			if p.DryRun {
				// 预演时仅告知"将删除库记录"
				if r.Reason == "" {
					r.Reason = "将删除库记录"
				}
			}
		}
		results = append(results, r)
	}

	if !p.DryRun && len(newEntries) > 0 {
		s.mu.Lock()
		entries := append(s.readTrashIndex(), newEntries...)
		err := s.writeTrashIndex(entries)
		s.mu.Unlock()
		if err != nil {
			log.Printf("写回收站索引失败: %v", err)
		}
		// 软删后把本地文件属主归位（避免 root 拥有）
		chownTree(filepath.Join(cfg.TrashDir, "files"))
	}

	summary := map[string]int{"total": len(results)}
	for _, r := range results {
		summary[r.Local]++
		if r.Remote == "ok" || r.Remote == "planned" || r.Remote == "failed" {
			summary["remote_"+r.Remote]++
		}
	}
	if !p.DryRun {
		s.audit("delete items=%d scope={library:%v,local:%v,remote:%v} soft=%v",
			len(p.Items), p.Scope.Library, p.Scope.Local, p.Scope.Remote, p.Soft)
	}
	return map[string]any{"dryRun": p.DryRun, "soft": p.Soft, "results": results, "summary": summary}, nil
}

func newEntryID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// moveFile 移动文件（跨设备时退化为复制+删除）。
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// ---------------------------------------------------------------------------
// 回收站：列表 / 恢复 / 彻底删除 / 超期清理
// ---------------------------------------------------------------------------

func (s *Server) actionTrashList() (any, error) {
	entries := s.readTrashIndex()
	return map[string]any{"entries": entries, "count": len(entries)}, nil
}

// actionTrashRestore 从回收站恢复指定条目（本地与网盘同时归位）。
func (s *Server) actionTrashRestore(ids []string) (any, error) {
	if len(ids) == 0 {
		return nil, errors.New("未指定要恢复的条目")
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}

	s.mu.Lock()
	cfg := s.cfg
	entries := s.readTrashIndex()
	s.mu.Unlock()

	restored := []string{}
	kept := []TrashEntry{}
	for _, e := range entries {
		if !want[e.ID] {
			kept = append(kept, e)
			continue
		}
		var errs []string
		if e.TrashedLocal != "" {
			if _, err := os.Stat(e.TrashedLocal); err == nil {
				if err := moveFile(e.TrashedLocal, e.LocalPath); err != nil {
					errs = append(errs, "本地恢复失败: "+err.Error())
				}
			}
		}
		if e.TrashedRemote != "" && e.RemotePath != "" {
			if _, err := s.baidupcs("mv", e.TrashedRemote, e.RemotePath); err != nil {
				errs = append(errs, "网盘恢复失败: "+err.Error())
			}
		}
		if len(errs) == 0 {
			restored = append(restored, e.ID)
		} else {
			log.Printf("恢复条目 %s 部分失败: %v", e.ID, errs)
			kept = append(kept, e)
		}
	}

	s.mu.Lock()
	_ = s.writeTrashIndex(kept)
	s.mu.Unlock()
	chownTree(cfg.LocalRoot)
	s.audit("trash_restore ids=%v restored=%d", ids, len(restored))
	return map[string]any{"restored": restored, "count": len(restored)}, nil
}

// actionTrashPurge 彻底删除回收站条目（可选同时清理网盘回收目录）。
func (s *Server) actionTrashPurge(ids []string, deleteRemote bool) (any, error) {
	s.mu.Lock()
	entries := s.readTrashIndex()
	s.mu.Unlock()

	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	purged := 0
	kept := []TrashEntry{}
	for _, e := range entries {
		if !want[e.ID] {
			kept = append(kept, e)
			continue
		}
		if e.TrashedLocal != "" {
			_ = os.Remove(e.TrashedLocal)
		}
		if deleteRemote && e.TrashedRemote != "" {
			if _, err := s.baidupcs("rm", e.TrashedRemote); err != nil {
				log.Printf("清理网盘回收文件失败 %s: %v", e.TrashedRemote, err)
				kept = append(kept, e) // 未清理成功则保留条目
				continue
			}
		}
		purged++
	}
	s.mu.Lock()
	_ = s.writeTrashIndex(kept)
	s.mu.Unlock()
	s.audit("trash_purge ids=%v purged=%d deleteRemote=%v", ids, purged, deleteRemote)
	return map[string]any{"purged": purged}, nil
}

// cleanupTrashLoop 周期性清理超期回收项（默认保留 30 天）。
func (s *Server) cleanupTrashLoop() {
	// 启动后先等一会儿再清，避免与启动流程抢 IO
	time.Sleep(2 * time.Minute)
	for {
		s.mu.Lock()
		retention := s.cfg.RetentionDays
		s.mu.Unlock()
		if retention > 0 {
			deadline := time.Now().AddDate(0, 0, -retention)
			entries := s.readTrashIndex()
			var ids []string
			for _, e := range entries {
				if e.DeletedAt.Before(deadline) {
					ids = append(ids, e.ID)
				}
			}
			if len(ids) > 0 {
				log.Printf("超期回收站清理：%d 项（保留 %d 天）", len(ids), retention)
				if _, err := s.actionTrashPurge(ids, false); err != nil {
					log.Printf("超期清理失败: %v", err)
				}
			}
		}
		time.Sleep(6 * time.Hour)
	}
}

// chownTree 把目录树属主改为 navidrome:navidrome（下载/恢复后调用，保证扫描器可读）。
func chownTree(root string) {
	grp, err := user.LookupGroup("navidrome")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return
	}
	usr, err := user.Lookup("navidrome")
	if err != nil {
		return
	}
	uid, err := strconv.Atoi(usr.Uid)
	if err != nil {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Lchown(p, uid, gid)
		return nil
	})
}
