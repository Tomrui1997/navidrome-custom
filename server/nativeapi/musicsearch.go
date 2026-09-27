package nativeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/core/baidusync"
	"github.com/navidrome/navidrome/log"
)

// 在线搜索实现说明：
//   - 搜索/歌词/试听由 Navidrome 直连回环上的 go-music-api（只读、可并发）；
//     **下载**则交给 root 助手执行，以复用其进度/取消/日志/审计与文件属主处理。
//   - 本路由组必须挂在 adminOnlyMiddleware 之内（见 native_api.go）：搜索会出网访问第三方
//     音乐平台，属于管理员专用能力。
//   - 助手只接收歌曲 id/source，直链由助手自行换取；本层对 id/source 做白名单校验，
//     避免把任意 URL 或异常标识透传下去。

// musicSearchSources 支持的平台（与 go-music-api 一致）。
var musicSearchSources = map[string]bool{
	"netease": true, "qq": true, "kugou": true, "kuwo": true, "migu": true,
	"qianqian": true, "fivesing": true, "jamendo": true, "joox": true,
	"bilibili": true, "soda": true,
}

// musicIDRe 歌曲 id 白名单：各平台 id 可能含数字、字母、点、横线、下划线，咪咕还有竖线。
var musicIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-|]{1,128}$`)

const (
	maxMusicKeywordRunes = 100
	maxMusicItems        = 200 // 单次提交下载上限（与助手侧一致）
	maxMusicBodyBytes    = 1 << 20
)

// addMusicSearchRoute 注册「在线搜索」相关路由。
func (api *Router) addMusicSearchRoute(r chi.Router) {
	client := baidusync.NewClient(baidusync.SocketPathFromEnv())

	r.Route("/musicsearch", func(r chi.Router) {
		r.Get("/health", handleMusicSearchHealth(client))
		r.Get("/search", handleMusicSearch(client))
		r.Get("/lyric", handleMusicSearchLyric(client))
		r.Post("/download", handleMusicDownloadStart(client))
		r.Get("/download/status", handleMusicDownloadStatus(client))
		r.Post("/download/cancel", handleMusicDownloadCancel(client))
	})
}

// ---------------------------------------------------------------------------
// 健康检查
// ---------------------------------------------------------------------------

// handleMusicSearchHealth 返回在线搜索可用性，供页面顶部展示与前端决定是否禁用入口。
func handleMusicSearchHealth(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := client.GetConfig(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		reachable, detail := probeMusicAPI(r.Context(), *cfg)
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":   cfg.MusicApiEnabled,
			"baseURL":   cfg.MusicApiBaseURL,
			"sources":   cfg.MusicApiSources,
			"types":     cfg.DownloadTypes,
			"dir":       cfg.DownloadDir,
			"preview":   cfg.EnablePreview,
			"reachable": reachable,
			"detail":    detail,
		})
	}
}

// probeMusicAPI 探测 go-music-api 是否可用（只打一个轻量接口）。
func probeMusicAPI(ctx context.Context, cfg baidusync.Config) (bool, string) {
	if !cfg.MusicApiEnabled {
		return false, "在线搜索未启用"
	}
	base, err := musicAPIBase(cfg)
	if err != nil {
		return false, err.Error()
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/api/v1/system/cookies", nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false, "在线搜索服务不可用: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("在线搜索服务返回 %d", resp.StatusCode)
	}
	return true, ""
}

// musicAPIBase 校验并返回 go-music-api 地址。
// **只允许回环**：即使助手配置被改坏，也不能让本服务成为访问外部主机的跳板（SSRF）。
func musicAPIBase(cfg baidusync.Config) (string, error) {
	raw := strings.TrimSpace(cfg.MusicApiBaseURL)
	if raw == "" {
		return "", errors.New("未配置 go-music-api 地址")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("go-music-api 地址格式不合法")
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return "", fmt.Errorf("go-music-api 地址只允许回环（当前 %s）", u.Hostname())
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// callMusicAPI 调用 go-music-api 并返回原始响应体（透传给前端，少一层结构耦合）。
func callMusicAPI(ctx context.Context, cfg baidusync.Config, path string, q url.Values) (json.RawMessage, error) {
	base, err := musicAPIBase(cfg)
	if err != nil {
		return nil, err
	}
	if !cfg.MusicApiEnabled {
		return nil, errors.New("在线搜索未启用")
	}
	endpoint := base + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	timeout := cfg.MusicApiTimeoutSec
	if timeout < 5 || timeout > 60 {
		timeout = 15 // 配置缺失或越界时回落
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("在线搜索服务不可用: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 上限 8MB，防异常大响应
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("在线搜索服务返回 %d", resp.StatusCode)
	}
	return json.RawMessage(body), nil
}

// ---------------------------------------------------------------------------
// 搜索 / 歌词
// ---------------------------------------------------------------------------

// parseMusicSources 解析平台参数，过滤为白名单内的值。
func parseMusicSources(raw, fallback string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || !musicSearchSources[s] || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func handleMusicSearch(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := client.GetConfig(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少搜索关键词"})
			return
		}
		if len([]rune(q)) > maxMusicKeywordRunes {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("关键词过长（上限 %d 个字符）", maxMusicKeywordRunes)})
			return
		}
		searchType := strings.TrimSpace(r.URL.Query().Get("type"))
		if searchType == "" {
			searchType = "song"
		}
		switch searchType {
		case "song", "album", "playlist":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索类型只能是 song / album / playlist"})
			return
		}
		sources := parseMusicSources(r.URL.Query().Get("sources"), cfg.MusicApiSources)
		if len(sources) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可用的搜索平台"})
			return
		}

		params := url.Values{}
		params.Set("q", q)
		params.Set("type", searchType)
		for _, s := range sources {
			params.Add("sources", s)
		}
		res, err := callMusicAPI(r.Context(), *cfg, "/api/v1/music/search", params)
		if err != nil {
			writeMusicError(w, r.Context(), err)
			return
		}
		log.Debug(r.Context(), "在线搜索完成", "q", q, "sources", sources)
		writeJSON(w, http.StatusOK, res)
	}
}

func handleMusicSearchLyric(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := client.GetConfig(r.Context())
		if err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		q := r.URL.Query()
		id, source := strings.TrimSpace(q.Get("id")), strings.ToLower(strings.TrimSpace(q.Get("source")))
		if !musicIDRe.MatchString(id) || !musicSearchSources[source] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id 或 source 不合法"})
			return
		}
		params := url.Values{}
		params.Set("id", id)
		params.Set("source", source)
		res, err := callMusicAPI(r.Context(), *cfg, "/api/v1/music/lyric", params)
		if err != nil {
			writeMusicError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// ---------------------------------------------------------------------------
// 下载（转发给 root 助手执行）
// ---------------------------------------------------------------------------

func handleMusicDownloadStart(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Items []baidusync.DownloadItem `json:"items"`
			Types []string                 `json:"types"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxMusicBodyBytes)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
			return
		}
		if len(body.Items) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未选择任何歌曲"})
			return
		}
		if len(body.Items) > maxMusicItems {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("单次最多下载 %d 首", maxMusicItems)})
			return
		}
		// 逐项校验：id 形状 + 平台白名单（不接受任何 URL 字段，防 SSRF / 越权写入）
		for i, it := range body.Items {
			if !musicIDRe.MatchString(strings.TrimSpace(it.ID)) {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("第 %d 项的歌曲 id 不合法", i+1)})
				return
			}
			if !musicSearchSources[strings.ToLower(strings.TrimSpace(it.Source))] {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("第 %d 项的平台不合法", i+1)})
				return
			}
			body.Items[i].Source = strings.ToLower(strings.TrimSpace(it.Source))
			body.Items[i].ID = strings.TrimSpace(it.ID)
		}

		var out map[string]any
		if err := client.Call(r.Context(), "download_start", map[string]any{
			"items": body.Items,
			"types": body.Types,
		}, &out); err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Info(r.Context(), "已提交在线下载任务", "count", len(body.Items), "types", body.Types)
		writeJSON(w, http.StatusOK, out)
	}
}

func handleMusicDownloadStatus(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var st baidusync.Status
		if err := client.Call(r.Context(), "status", nil, &st); err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		writeJSON(w, http.StatusOK, st.Download)
	}
}

func handleMusicDownloadCancel(client *baidusync.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := client.Call(r.Context(), "download_cancel", nil, nil); err != nil {
			writeHelperError(w, r.Context(), err)
			return
		}
		log.Warn(r.Context(), "已请求取消在线下载")
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
	}
}

// writeMusicError 上游服务不可用时返回 502（与助手异常区分开）。
func writeMusicError(w http.ResponseWriter, ctx context.Context, err error) {
	log.Error(ctx, "在线搜索上游调用失败", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}
