package nativeapi

import (
	"encoding/json"
	"net/http"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/baidusync"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model/request"
)

// addMediaOpsRoute 注册媒体库管理路由（删除 / 回收站 / 恢复）。
//
// 必须挂在 adminOnlyMiddleware 之内：涉及文件系统与网盘写操作。
func (api *Router) addMediaOpsRoute(r chi.Router) {
	client := baidusync.NewClient(baidusync.SocketPathFromEnv())

	r.Route("/mediaops", func(r chi.Router) {
		r.Post("/delete", api.handleMediaDelete(client))
		r.Get("/trash", api.handleTrashList(client))
		r.Post("/restore", api.handleTrashRestore(client))
		r.Post("/purge", api.handleTrashPurge(client))
	})
}

// mediaDeleteRequest 删除请求体。
type mediaDeleteRequest struct {
	IDs    []string              `json:"ids"`
	Scope  baidusync.DeleteScope `json:"scope"`
	Soft   *bool                 `json:"soft"`
	DryRun bool                  `json:"dryRun"`
}

// handleMediaDelete 删除曲目：
//   - 文件系统与网盘侧交由 root 助手处理（支持软删除，进 trash/ 与网盘 /_deleted/）；
//   - 数据库记录由本服务删除（助手返回 itemID）；
//   - dryRun=true 时只回报将执行的动作，不做任何变更。
func (api *Router) handleMediaDelete(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body mediaDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
			return
		}
		if len(body.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未指定要删除的曲目"})
			return
		}
		if !body.Scope.Library && !body.Scope.Local && !body.Scope.Remote {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "删除范围不能为空"})
			return
		}

		cfg, err := client.GetConfig(ctx)
		if err != nil {
			writeHelperError(w, ctx, err)
			return
		}

		// 组装待删除项：由库中记录反查本地路径，再映射为网盘路径
		repo := api.ds.MediaFile(ctx)
		items := make([]baidusync.DeleteItem, 0, len(body.IDs))
		results := make([]baidusync.DeleteResult, 0, len(body.IDs))
		for _, id := range body.IDs {
			mf, gerr := repo.Get(id)
			if gerr != nil {
				results = append(results, baidusync.DeleteResult{
					ID: id, Local: "failed", Remote: "failed",
					Reason: "库中未找到该曲目: " + gerr.Error(),
				})
				continue
			}
			// 注意：mf.Path 是相对媒体库根的路径，必须用 AbsolutePath() 取出真实文件路径，
			// 否则传给助手会被「路径超出允许范围」的安全检查拒绝。
			abs := mf.AbsolutePath()
			item := baidusync.DeleteItem{ID: id, Title: mf.Title, LocalPath: abs}
			// 网盘路径 = 网盘根 + 相对本地媒体库根的路径；
			// 库根与助手配置的 localRoot 不一致时不做映射（宁可跳过，不可误删网盘文件）。
			if rel, rerr := filepath.Rel(cfg.LocalRoot, abs); rerr == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				item.RemotePath = pathpkg.Join(cfg.RemoteRoot, filepath.ToSlash(rel))
			}
			items = append(items, item)
		}

		deletedBy := "unknown"
		if u, ok := request.UserFrom(ctx); ok {
			deletedBy = u.UserName
		}

		soft := cfg.SoftDelete
		if body.Soft != nil {
			soft = *body.Soft
		}
		// 配置要求软删时，禁止硬删（安全兜底）
		if cfg.SoftDelete {
			soft = true
		}

		var helperRes *baidusync.DeleteResponse
		if len(items) > 0 {
			helperRes, err = client.Delete(ctx, items, body.Scope, soft, body.DryRun, deletedBy)
			if err != nil {
				writeHelperError(w, ctx, err)
				return
			}
			results = append(results, helperRes.Results...)
		}

		// 删除库记录（仅非预演、且文件侧未硬失败时）
		dbDeleted := 0
		if !body.DryRun && body.Scope.Library {
			for _, res := range results {
				if res.Local == "failed" || res.Remote == "failed" {
					log.Warn(ctx, "跳过库记录删除（文件侧失败）", "id", res.ID, "reason", res.Reason)
					continue
				}
				if derr := repo.Delete(res.ID); derr != nil {
					log.Error(ctx, "删除库记录失败", "id", res.ID, derr)
					continue
				}
				dbDeleted++
			}
			log.Info(ctx, "删除曲目完成", "请求数", len(body.IDs), "库记录删除", dbDeleted,
				"范围", map[string]bool{"library": body.Scope.Library, "local": body.Scope.Local, "remote": body.Scope.Remote},
				"软删", soft, "操作人", deletedBy)
		}

		summary := map[string]int{}
		if helperRes != nil {
			summary = helperRes.Summary
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"dryRun":    body.DryRun,
			"soft":      soft,
			"results":   results,
			"summary":   summary,
			"dbDeleted": dbDeleted,
		})
	}
}

func (api *Router) handleTrashList(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := client.TrashList(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
	}
}

func (api *Router) handleTrashRestore(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未指定要恢复的条目"})
			return
		}
		n, err := client.TrashRestore(r.Context(), body.IDs)
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Info(r.Context(), "回收站恢复完成", "数量", n)
		writeJSON(w, http.StatusOK, map[string]any{"restored": n})
	}
}

func (api *Router) handleTrashPurge(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs          []string `json:"ids"`
			DeleteRemote bool     `json:"deleteRemote"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未指定要彻底删除的条目"})
			return
		}
		n, err := client.TrashPurge(r.Context(), body.IDs, body.DeleteRemote)
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Warn(r.Context(), "回收站彻底删除完成", "数量", n, "同时删网盘", body.DeleteRemote)
		writeJSON(w, http.StatusOK, map[string]any{"purged": n})
	}
}
