//go:build linux

package main

import "syscall"

// newSysProcAttr Linux 下让子进程单独成组（Setpgid），从而能整组终止。
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup 向进程组发送 SIGTERM（pid 为组内首进程 pid，取负即进程组）。
func terminateGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

// killGroup 强杀进程组。
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// processAlive 判断进程（组）是否仍存在：signal 0 只做存在性探测。
func processAlive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil
}
