//go:build windows

package main

import (
	"os"
	"syscall"
)

// 本助手只在 Linux（统信 UOS / CentOS）上以 root 运行；Windows 下仅用于编译期与单测，
// 因此进程组相关操作退化为直接终止单进程（Windows 无进程组语义，syscall.Kill 也不可用）。

// newSysProcAttr Windows 无进程组概念，返回 nil。
func newSysProcAttr() *syscall.SysProcAttr {
	return nil
}

// terminateGroup Windows 下直接 Kill（无优雅信号语义）。
func terminateGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// killGroup Windows 下同样直接 Kill。
func killGroup(pid int) {
	terminateGroup(pid)
}

// processAlive Windows 下无法用 signal 0 探测，保守返回 true（调用方随后会强杀）。
func processAlive(pid int) bool {
	return true
}
