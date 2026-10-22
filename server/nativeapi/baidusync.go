package nativeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/baidusync"
	"github.com/navidrome/navidrome/log"
)

// addBaiduSyncRoute 注册百度网盘同步相关路由。
//
// 该路由组必须挂在 adminOnlyMiddleware 之内（由 native_api.go 保证）：
// 同步涉及网盘凭据与文件系统写操作，仅管理员可用。
func (api *Router) addBaiduSyncRoute(r chi.Router) {
	client := baidusync.NewClient(baidusync.SocketPathFromEnv())

	r.Route("/baidusync", func(r chi.Router) {
		r.Get("/status", handleSyncStatus(client))
		r.Post("/login", handleSyncLogin(client))
		r.Post("/sync", handleSyncStart(client))
		r.Post("/cancel", handleSyncCancel(client))
		r.Get("/config", handleSyncConfigGet(client))
		r.Put("/config", handleSyncConfigSet(client))
		r.Get("/log", handleSyncLog(client))
	})
}

// writeJSON 统一的 JSON 响应写法。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Error("写响应失败", err)
	}
}

// writeHelperError 助手不可用或执行失败时返回 502，便于前端区分「参数错误」与「助手异常」。
func writeHelperError(w http.ResponseWriter, ctx context.Context, err error) {
	log.Error(ctx, "同步助手调用失败", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

func handleSyncStatus(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := client.GetStatus(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// handleSyncLogin 用页面提交的 Cookie / BDUSS 登录百度网盘。
//
// 解决「cookie 过期后必须上服务器执行脚本才能重新登录」的老问题。
// 凭据只在请求体里经过一次内存，不落库、不记日志。
func handleSyncLogin(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Cookie string `json:"cookie"`
			BDUSS  string `json:"bduss"`
			SToken string `json:"stoken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
			return
		}
		res, err := client.LoginCookie(r.Context(), body.Cookie, body.BDUSS, body.SToken)
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Info(r.Context(), "已更新百度网盘登录凭据", "uid", res.UID, "verified", res.Verified)
		writeJSON(w, http.StatusOK, res)
	}
}

func handleSyncStart(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode  string   `json:"mode"`
			Types []string `json:"types"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
			return
		}
		if body.Mode == "" {
			body.Mode = "incremental"
		}
		if err := client.StartSync(r.Context(), body.Mode, body.Types); err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Info(r.Context(), "已触发百度网盘同步", "mode", body.Mode, "types", body.Types)
		writeJSON(w, http.StatusOK, map[string]any{"started": true, "mode": body.Mode})
	}
}

func handleSyncCancel(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := client.CancelSync(r.Context()); err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Warn(r.Context(), "已请求取消百度网盘同步")
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
	}
}

func handleSyncConfigGet(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := client.GetConfig(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	}
}

func handleSyncConfigSet(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cfg baidusync.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "配置解析失败: " + err.Error()})
			return
		}
		saved, err := client.SetConfig(r.Context(), cfg)
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Info(r.Context(), "同步配置已更新", "remoteRoot", saved.RemoteRoot, "localRoot", saved.LocalRoot)
		writeJSON(w, http.StatusOK, saved)
	}
}

func handleSyncLog(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lines := 200
		if v := r.URL.Query().Get("lines"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				lines = n
			}
		}
		date := r.URL.Query().Get("date") // 可选 YYYY-MM-DD，缺省今天
		out, actualDate, files, err := client.LogTail(r.Context(), lines, date)
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"lines": out,
			"date":  actualDate,
			"files": files, // 现有日志日期列表，供页面切换查看
		})
	}
}
