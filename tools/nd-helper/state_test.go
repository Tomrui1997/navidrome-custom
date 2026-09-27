package main

import (
	"path/filepath"
	"testing"
	"time"
)

// 中断检测：进程在同步途中被杀（升级/重启）时，必须把状态标记为「被中断」，
// 否则前端只显示上一次成功时间，用户无法判断任务是否还在跑。
func TestMarkInterruptedSync(t *testing.T) {
	dir := t.TempDir()

	// 场景一：已开始但未结束 → 必须标记
	s1 := &Server{statePath: filepath.Join(dir, "state1.json")}
	s1.state.LastSyncStart = time.Now()
	s1.state.LastSyncEnd = time.Now().Add(-time.Hour)
	s1.state.LastSyncOK = true
	s1.markInterruptedSync()
	if s1.state.LastSyncOK {
		t.Error("中断的同步不应标记为成功")
	}
	if s1.state.LastSyncMsg == "" {
		t.Error("中断的同步必须有说明文案")
	}

	// 场景二：已正常结束 → 不得误报
	s2 := &Server{statePath: filepath.Join(dir, "state2.json")}
	s2.state.LastSyncStart = time.Now().Add(-time.Minute)
	s2.state.LastSyncEnd = time.Now()
	s2.state.LastSyncOK = true
	s2.markInterruptedSync()
	if !s2.state.LastSyncOK || s2.state.LastSyncMsg != "" {
		t.Errorf("正常结束的同步被误判为中断: ok=%v msg=%q", s2.state.LastSyncOK, s2.state.LastSyncMsg)
	}

	// 场景三：从未同步过 → 不产生任何提示
	s3 := &Server{statePath: filepath.Join(dir, "state3.json")}
	s3.markInterruptedSync()
	if s3.state.LastSyncMsg != "" {
		t.Errorf("从未同步过不应产生中断提示: %q", s3.state.LastSyncMsg)
	}
}
