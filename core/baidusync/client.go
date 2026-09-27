// Package baidusync 提供与特权助手 nd-helper 的通信客户端。
//
// 设计要点：
//   - Navidrome 以 navidrome 用户运行，不接触百度网盘凭据；
//   - 所有特权动作（下载/删除/移动/配置）都通过本客户端请求 root 助手执行；
//   - 通信走本地 Unix socket，一问一答，超时保护。
package baidusync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// DefaultSocketPath 助手默认监听的 socket 路径，可用环境变量覆盖。
const DefaultSocketPath = "/run/nd-helper.sock"

// SocketPathFromEnv 返回助手 socket 路径（支持 ND_BAIDUSYNC_SOCKET 覆盖）。
func SocketPathFromEnv() string {
	if p := os.Getenv("ND_BAIDUSYNC_SOCKET"); p != "" {
		return p
	}
	return DefaultSocketPath
}

// Client 助手客户端。
type Client struct {
	SocketPath string
	Timeout    time.Duration
}

// NewClient 创建客户端。
func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = SocketPathFromEnv()
	}
	return &Client{SocketPath: socketPath, Timeout: 30 * time.Second}
}

type request struct {
	Action string `json:"action"`
	Params any    `json:"params,omitempty"`
}

type response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// Call 执行一次助手动作，并把结果解码到 out（out 可为 nil）。
func (c *Client) Call(ctx context.Context, action string, params any, out any) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("无法连接同步助手（%s）: %w", c.SocketPath, err)
	}
	defer conn.Close()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	payload, err := json.Marshal(request{Action: action, Params: params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}

	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "助手返回未知错误"
		}
		return errors.New(resp.Error)
	}
	if out == nil || len(resp.Data) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Data, out)
}

// ---------------------------------------------------------------------------
// 业务数据结构（与助手保持一致）
// ---------------------------------------------------------------------------

// Schedule 定时配置。
type Schedule struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	At      string `json:"at"`
	Minutes int    `json:"minutes"`
}

// Config 同步与删除配置。
type Config struct {
	Enabled       bool   `json:"enabled"`
	BaidupcsBin   string `json:"baidupcsBin"`
	RemoteRoot    string `json:"remoteRoot"`
	LocalRoot     string `json:"localRoot"`
	IncludeLyrics bool   `json:"includeLyrics"`
	IncludeCovers bool   `json:"includeCovers"`
	// ExcludeDirs 远端根下要跳过的目录（相对路径），同步只抓歌曲/歌词/封面。
	ExcludeDirs []string `json:"excludeDirs"`
	// LockPaths 锁定 remoteRoot/localRoot，防止误改导致同步范围跑偏。
	LockPaths      bool     `json:"lockPaths"`
	Threads        int      `json:"threads"`
	ParallelFiles  int      `json:"parallelFiles"`
	DeleteExtra    bool     `json:"deleteExtra"`
	Schedule       Schedule `json:"schedule"`
	SoftDelete     bool     `json:"softDelete"`
	TrashDir       string   `json:"trashDir"`
	RemoteTrashDir string   `json:"remoteTrashDir"`
	RetentionDays  int      `json:"retentionDays"`
	// LogRetentionDays 同步日志（按天分文件）保留天数，默认 7。
	LogRetentionDays int `json:"logRetentionDays"`
}

// Login 百度登录态。
type Login struct {
	LoggedIn bool   `json:"loggedIn"`
	UID      string `json:"uid"`
	UserName string `json:"userName"`
	Error    string `json:"error"`
}

// Task 同步任务状态。
type Task struct {
	Running   bool     `json:"running"`
	Mode      string   `json:"mode"`
	Trigger   string   `json:"trigger"`
	Types     []string `json:"types"`
	Elapsed   int      `json:"elapsed"`
	FilesDone int      `json:"filesDone"`
	FilesSeen int      `json:"filesSeen"`
	ErrMsg    string   `json:"errMsg"`
	StartedAt string   `json:"startedAt"`
}

// Status 助手整体状态。
type Status struct {
	Login       Login  `json:"login"`
	Task        Task   `json:"task"`
	Config      Config `json:"config"`
	NextRun     string `json:"nextRun"`
	LastSync    string `json:"lastSync"`
	LastSyncOK  bool   `json:"lastSyncOK"`
	LastSyncMsg string `json:"lastSyncMsg"`
	TrashCount  int    `json:"trashCount"`
}

// DeleteScope 删除范围。
type DeleteScope struct {
	Library bool `json:"library"`
	Local   bool `json:"local"`
	Remote  bool `json:"remote"`
}

// DeleteItem 待删除项。
type DeleteItem struct {
	ID         string `json:"id"`
	LocalPath  string `json:"localPath"`
	RemotePath string `json:"remotePath"`
	Title      string `json:"title"`
}

// DeleteResult 单项结果。
type DeleteResult struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Reason string `json:"reason"`
	// RemoteTarget 预演时回传的网盘目标路径（用于删除前核对）。
	RemoteTarget string `json:"remoteTarget,omitempty"`
}

// DeleteResponse 删除结果汇总。
type DeleteResponse struct {
	DryRun  bool           `json:"dryRun"`
	Soft    bool           `json:"soft"`
	Results []DeleteResult `json:"results"`
	Summary map[string]int `json:"summary"`
}

// TrashEntry 回收站条目。
type TrashEntry struct {
	ID            string `json:"id"`
	ItemID        string `json:"itemId"`
	Title         string `json:"title"`
	RelPath       string `json:"relPath"`
	LocalPath     string `json:"localPath"`
	TrashedLocal  string `json:"trashedLocal"`
	RemotePath    string `json:"remotePath"`
	TrashedRemote string `json:"trashedRemote"`
	Size          int64  `json:"size"`
	DeletedAt     string `json:"deletedAt"`
	DeletedBy     string `json:"deletedBy"`
}

// ---------------------------------------------------------------------------
// 便捷方法
// ---------------------------------------------------------------------------

// GetStatus 读取助手状态。
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.Call(ctx, "status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// GetConfig 读取配置。
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := c.Call(ctx, "config_get", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetConfig 保存配置。
func (c *Client) SetConfig(ctx context.Context, cfg Config) (*Config, error) {
	var saved struct {
		Saved  bool   `json:"saved"`
		Config Config `json:"config"`
	}
	if err := c.Call(ctx, "config_set", cfg, &saved); err != nil {
		return nil, err
	}
	return &saved.Config, nil
}

// StartSync 触发同步。
func (c *Client) StartSync(ctx context.Context, mode string, types []string) error {
	params := map[string]any{"mode": mode}
	if len(types) > 0 {
		params["types"] = types
	}
	return c.Call(ctx, "sync_start", params, nil)
}

// CancelSync 取消当前同步。
func (c *Client) CancelSync(ctx context.Context) error {
	return c.Call(ctx, "sync_cancel", nil, nil)
}

// LogTail 读取同步日志末尾（date 为空表示今天）。返回日志行、实际日期与可选的日期清单。
func (c *Client) LogTail(ctx context.Context, lines int, date string) ([]string, string, []string, error) {
	var res struct {
		Lines []string `json:"lines"`
		Date  string   `json:"date"`
		Files []string `json:"files"`
	}
	params := map[string]any{"lines": lines}
	if date != "" {
		params["date"] = date
	}
	if err := c.Call(ctx, "log_tail", params, &res); err != nil {
		return nil, "", nil, err
	}
	return res.Lines, res.Date, res.Files, nil
}

// Delete 请求助手处理本地/网盘文件（库记录由调用方删除）。
func (c *Client) Delete(ctx context.Context, items []DeleteItem, scope DeleteScope, soft, dryRun bool, deletedBy string) (*DeleteResponse, error) {
	params := map[string]any{
		"items":     items,
		"scope":     scope,
		"soft":      soft,
		"dryRun":    dryRun,
		"deletedBy": deletedBy,
	}
	var res DeleteResponse
	if err := c.Call(ctx, "delete", params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// TrashList 列出回收站条目。
func (c *Client) TrashList(ctx context.Context) ([]TrashEntry, error) {
	var res struct {
		Entries []TrashEntry `json:"entries"`
		Count   int          `json:"count"`
	}
	if err := c.Call(ctx, "trash_list", nil, &res); err != nil {
		return nil, err
	}
	return res.Entries, nil
}

// TrashRestore 恢复条目。
func (c *Client) TrashRestore(ctx context.Context, ids []string) (int, error) {
	var res struct {
		Restored []string `json:"restored"`
		Count    int      `json:"count"`
	}
	if err := c.Call(ctx, "trash_restore", map[string]any{"ids": ids}, &res); err != nil {
		return 0, err
	}
	return res.Count, nil
}

// TrashPurge 彻底删除回收站条目。
func (c *Client) TrashPurge(ctx context.Context, ids []string, deleteRemote bool) (int, error) {
	var res struct {
		Purged int `json:"purged"`
	}
	params := map[string]any{"ids": ids, "deleteRemote": deleteRemote}
	if err := c.Call(ctx, "trash_purge", params, &res); err != nil {
		return 0, err
	}
	return res.Purged, nil
}
