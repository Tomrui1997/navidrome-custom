package nativeapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/model"
)

// 本地重复歌曲检测（仅管理员）。
//
// 为什么独立成文件：检测要遍历整个媒体库并读取文件头尾计算指纹，与 mediaops
// （删除 / 回收站）职责不同，拆开便于单独维护。
//
// 安全边界：本接口**只读**，不做任何删除。清理动作由前端复用
// POST /api/mediaops/delete —— 该接口已有 dryRun 预演、软删兜底、路径越界校验与审计日志。

var (
	// 不可见字符（零宽空格、方向标记、字节序标记等）
	dupInvisibleRe = regexp.MustCompile(`[\x{200b}\x{200e}\x{200f}\x{180e}\x{feff}\x{2060}]`)
	// 非 ASCII 空白（不换行空格、全角空格、各种窄空格）
	dupUnicodeSpaceRe = regexp.MustCompile("[\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]")
	dupSpaceRe        = regexp.MustCompile(`\s+`)
)

// dupFingerprintChunk 指纹取样窗口：头尾各取这么多字节。
// 为什么不全量 MD5：大库全量读取磁盘代价过高；「文件大小 + 头 1MB + 尾 1MB」
// 已足以区分音乐文件（同一平台下载的相同音频必然一致）。
const dupFingerprintChunk = 1 << 20

// dupItem 重复组内的单个曲目。
type dupItem struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"` // 媒体库内相对路径（仅用于展示）
	Size        int64   `json:"size"`
	Bitrate     int     `json:"bitrate"`
	Duration    float32 `json:"duration"`
	Format      string  `json:"format"`
	Depth       int     `json:"depth"`       // 目录层级，越深表示结构越规整
	Exists      bool    `json:"exists"`      // 磁盘上是否真实存在
	Cluster     int     `json:"cluster"`     // 内容集群序号：同一集群内只有一项会被保留
	Keep        bool    `json:"keep"`        // 本集群内是否推荐保留
	SameContent bool    `json:"sameContent"` // 与同组保留项内容是否完全一致
	Reason      string  `json:"reason"`      // 保留/删除理由（展示给用户）
}

// dupGroup 一组重复歌曲（组内文件内容完全一致）。
type dupGroup struct {
	Key         string    `json:"key"`
	Artist      string    `json:"artist"`
	Title       string    `json:"title"`
	KeepID      string    `json:"keepId"`
	KeepIDs     []string  `json:"keepIds"`     // 每组「每个内容集群各一份」的全部推荐保留项
	SameContent bool      `json:"sameContent"` // 组内所有项内容是否完全一致
	NamesDiffer bool      `json:"namesDiffer"` // 组内歌名/艺人是否存在多个不同写法（内容相同但名称不同）
	Items       []dupItem `json:"items"`
}

// dupResponse 检测结果。
type dupResponse struct {
	Groups         []dupGroup `json:"groups"`
	Scanned        int        `json:"scanned"`
	GroupCount     int        `json:"groupCount"`
	RedundantCount int        `json:"redundantCount"`
	ElapsedMs      int64      `json:"elapsedMs"`
	Note           string     `json:"note"`
}

// dupNormalize 归一化用于分组的文本：剔除不可见字符与非 ASCII 空白、折叠空白、小写。
func dupNormalize(s string) string {
	s = dupInvisibleRe.ReplaceAllString(s, "")
	s = dupUnicodeSpaceRe.ReplaceAllString(s, "")
	s = dupSpaceRe.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// dupFileFingerprint 计算文件指纹：大小 + 头部 1MB + 尾部 1MB 的 MD5。
// 文件不存在或读失败时返回空串（该组会退化为「无法确认内容」，不参与自动删除判定）。
func dupFileFingerprint(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := md5.New()
	buf := make([]byte, dupFingerprintChunk)
	if n, rerr := io.ReadFull(f, buf); n > 0 && (rerr == nil || rerr == io.ErrUnexpectedEOF) {
		h.Write(buf[:n])
	}
	// 文件比取样窗口大时再补读尾部（小文件头尾重叠，跳过）
	if size > dupFingerprintChunk {
		if _, serr := f.Seek(-dupFingerprintChunk, io.SeekEnd); serr == nil {
			if n, rerr := io.ReadFull(f, buf); n > 0 && (rerr == nil || rerr == io.ErrUnexpectedEOF) {
				h.Write(buf[:n])
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dupBetterToKeep 判断 a 是否比 b 更值得保留（返回 true 表示保留 a）。
// 优先级：目录更深（结构规整） > 码率更高 > 文件更大 > 路径字典序（保证结果可复现）。
func dupBetterToKeep(a, b dupItem) bool {
	if a.Depth != b.Depth {
		return a.Depth > b.Depth
	}
	if a.Bitrate != b.Bitrate {
		return a.Bitrate > b.Bitrate
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return a.Path < b.Path
}

// collectDuplicates 执行一次完整检测，返回重复组、扫描总数、可清理的冗余数。
//
// 抽成独立函数的原因：/api/duplicates（展示）与 /api/duplicates/verify（清理前兜底校验）
// 必须共用同一份判定逻辑，否则一旦两处漂移，就可能出现「界面显示为保留、校验却允许删除」
// 这类危险的不一致。
func (api *Router) collectDuplicates(ctx context.Context) ([]dupGroup, int, int, error) {
	repo := api.ds.MediaFile(ctx)
	all, err := repo.GetAll()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("读取媒体库失败: %w", err)
	}

	// 1) 先按「文件大小」预分桶，再按「内容指纹」分组。
	//
	// 为什么不再按「艺人+歌名」分组：用户明确要求「名称不一样但文件大小完全一样的也要去重」——
	// 同一份音频被重命名后（带/不带后缀、不同平台命名）也必须识别为重复。
	// 先比大小可把绝大多数文件一次排除，只有大小相同的才需要读文件头尾算指纹。
	fpCache := map[string]string{} // id → 内容指纹（分组阶段算一次，后续复用）
	bySize := map[int64][]model.MediaFile{}
	for _, mf := range all {
		if mf.Size <= 0 {
			continue
		}
		bySize[mf.Size] = append(bySize[mf.Size], mf)
	}
	buckets := map[string][]model.MediaFile{}
	for _, bucket := range bySize {
		if len(bucket) < 2 {
			continue // 大小唯一 → 内容绝不可能相同
		}
		for _, mf := range bucket {
			fp := dupFileFingerprint(mf.AbsolutePath(), mf.Size)
			if fp == "" {
				continue // 读不到内容的不参与分组，绝不误删
			}
			buckets[fp] = append(buckets[fp], mf)
			fpCache[mf.ID] = fp
		}
	}

	keys := make([]string, 0, len(buckets))
	for k, v := range buckets {
		if len(v) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // 输出稳定，便于对比与排查

	groups := make([]dupGroup, 0, len(keys))
	redundant := 0

	for _, key := range keys {
		files := buckets[key]

		// 2) 组装基础信息（只对重复候选做文件 IO，避免全库读盘）
		items := make([]dupItem, 0, len(files))
		for _, mf := range files {
			abs := mf.AbsolutePath()
			st, serr := os.Stat(abs)
			it := dupItem{
				ID:       mf.ID,
				Path:     mf.Path,
				Size:     mf.Size,
				Bitrate:  mf.BitRate,
				Duration: mf.Duration,
				Format:   mf.Suffix,
				Depth:    strings.Count(filepath.ToSlash(mf.Path), "/"),
				Exists:   serr == nil,
			}
			if serr == nil {
				it.Size = st.Size() // 以磁盘实际大小为准（库记录可能过期）
			}
			items = append(items, it)
		}

		// 3) 按内容指纹切「内容集群」（指纹为空表示读取失败，单独成组不参与自动判定）
		clusters := map[string][]int{}
		order := []string{}
		for i, it := range items {
			fp := fpCache[it.ID] // 分组阶段已算过，直接复用，避免重复读盘
			if fp == "" && it.Exists {
				fp = dupFileFingerprint(files[i].AbsolutePath(), it.Size)
			}
			k := fp
			if k == "" {
				k = "unreadable\x00" + it.ID // 读不到内容的各算一份，绝不自动删
			}
			if _, ok := clusters[k]; !ok {
				order = append(order, k)
			}
			clusters[k] = append(clusters[k], i)
		}

		// 4) 每个「内容集群」各选一份保留，其余标记为待删除。
		// 同一集群内只留一份；不同集群（例如录音室版与 Live 版）各留一份 ——
		// 因此 UI 必须按 Cluster 维护保留项，不能假设「每组只保留一项」。
		keepIdx := map[int]bool{}
		clusterOf := make([]int, len(items))
		for ci, k := range order {
			idxs := clusters[k]
			best := idxs[0]
			for _, i := range idxs[1:] {
				if dupBetterToKeep(items[i], items[best]) {
					best = i
				}
			}
			keepIdx[best] = true
			for _, i := range idxs {
				clusterOf[i] = ci
			}
		}

		// 组内名称是否一致：一致则按「艺人 - 歌名」展示，不一致则提示「名称不同但内容一致」
		nameSet := map[string]bool{}
		artistSet := map[string]bool{}
		for _, mf := range files {
			nameSet[dupNormalize(mf.Title)] = true
			artistSet[dupNormalize(mf.Artist)] = true
		}
		group := dupGroup{
			Key:         key,
			Artist:      files[0].Artist,
			Title:       files[0].Title,
			SameContent: len(order) == 1 && !strings.HasPrefix(order[0], "unreadable"),
			NamesDiffer: len(nameSet) > 1 || len(artistSet) > 1,
			Items:       items,
		}
		for i := range items {
			items[i].Cluster = clusterOf[i]
			if keepIdx[i] {
				items[i].Keep = true
				items[i].SameContent = group.SameContent
				items[i].Reason = "保留：优先保留结构更规整（目录更深）且码率更高的一份"
				group.KeepIDs = append(group.KeepIDs, items[i].ID)
				if group.KeepID == "" {
					group.KeepID = items[i].ID
				}
				continue
			}
			items[i].Keep = false
			items[i].SameContent = group.SameContent
			if group.SameContent {
				items[i].Reason = "内容完全相同（文件大小与内容一致）的重复副本"
			} else {
				items[i].Reason = "同组内不同版本，已各保留一份，此项为冗余副本"
			}
			redundant++
		}
		group.Items = items
		groups = append(groups, group)
	}

	return groups, len(all), redundant, nil
}

// handleDuplicates 检测本地重复歌曲（只读），供页面展示。
//
// 分组规则：艺人 + 歌名归一化后相同 → 视为同一首歌；再按文件指纹切成若干个
// 「内容集群」，**每个内容集群各保留一份**，其余标记为待删除。
// 这样即使同组里混有不同版本（如录音室版与 Live 版），也能保证每个版本都留一份。
func (api *Router) handleDuplicates() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		groups, scanned, redundant, err := api.collectDuplicates(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, dupResponse{
			Groups:         groups,
			Scanned:        scanned,
			GroupCount:     len(groups),
			RedundantCount: redundant,
			ElapsedMs:      time.Since(start).Milliseconds(),
			Note:           "检测为只读操作；删除前请先调用 POST /api/duplicates/verify 取回允许删除的 id",
		})
	}
}

// dupVerifyRequest 清理前校验请求体。
type dupVerifyRequest struct {
	IDs []string `json:"ids"`
}

// dupVerifyResponse 校验结果：allowed 是**服务端确认可以删除**的 id，前端只能用这个列表去删除。
type dupVerifyResponse struct {
	Allowed  []string            `json:"allowed"`
	Rejected []map[string]string `json:"rejected"`
	KeepIDs  []string            `json:"keepIds"`
}

// handleDuplicatesVerify 清理前的服务端兜底校验（只读，不删任何东西）。
//
// 为什么需要：前端一旦有 bug 或误操作，可能把「每组 / 每内容集群的保留项」也提交上来
// （本次开发中就真实出现过一次这样的缺陷，靠预览拦下）。这里重新跑一遍检测，
// 把保留项以及不在冗余清单中的 id 全部挡掉，前端只能拿返回的 allowed 去删除。
func (api *Router) handleDuplicatesVerify() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body dupVerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败: " + err.Error()})
			return
		}

		groups, _, _, err := api.collectDuplicates(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		keep := map[string]bool{}
		redundantSet := map[string]bool{}
		keepIDs := []string{}
		for _, g := range groups {
			for _, it := range g.Items {
				if it.Keep {
					keep[it.ID] = true
					keepIDs = append(keepIDs, it.ID)
				} else {
					redundantSet[it.ID] = true
				}
			}
		}

		resp := dupVerifyResponse{Allowed: []string{}, Rejected: []map[string]string{}, KeepIDs: keepIDs}
		for _, id := range body.IDs {
			switch {
			case keep[id]:
				resp.Rejected = append(resp.Rejected, map[string]string{
					"id": id, "reason": "该曲目是所在重复组的保留项（每内容集群至少留一份），禁止删除"})
			case !redundantSet[id]:
				resp.Rejected = append(resp.Rejected, map[string]string{
					"id": id, "reason": "该曲目不在本次检测的冗余清单中"})
			default:
				resp.Allowed = append(resp.Allowed, id)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// addDuplicatesRoute 注册重复歌曲检测与清理前校验路由（挂载位置与 mediaops 同属管理员组）。
func (api *Router) addDuplicatesRoute(r chi.Router) {
	r.Get("/duplicates", api.handleDuplicates())
	// 清理前校验：把保留项挡在删除之外（真正的删除复用 /api/mediaops/delete）
	r.Post("/duplicates/verify", api.handleDuplicatesVerify())
}
