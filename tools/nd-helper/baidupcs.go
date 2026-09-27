package main

import (
	"fmt"
	"strings"
	"time"
)

// baidupcs 执行一次 baidupcs 命令（非交互、带超时），失败时携带原始输出便于排查。
func (s *Server) baidupcs(args ...string) ([]byte, error) {
	s.mu.Lock()
	bin := s.cfg.BaidupcsBin
	s.mu.Unlock()

	out, err := runCmd(10*time.Minute, nil, bin, args...)
	if err != nil {
		return out, fmt.Errorf("baidupcs %s 失败: %w（输出: %s）",
			strings.Join(args, " "), err, truncate(strings.TrimSpace(string(out)), 300))
	}
	return out, nil
}

// baidupcsMkdir 尽力创建网盘目录；目录已存在等情况不算失败（后续 mv 会给出真实错误）。
func (s *Server) baidupcsMkdir(dir string) ([]byte, error) {
	out, _ := s.baidupcs("mkdir", dir)
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
