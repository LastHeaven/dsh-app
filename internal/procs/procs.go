//go:build windows

// Package procs 是 node.exe 进程的枚举与命令行读取。
//
// 快照有缓存：同一进程里反复枚举容易出问题，抓一次存着，
// 需要新鲜数据的调用方显式失效。
package procs

import (
	"fmt"
	"sync"

	"github.com/LastHeaven/dsh-app/internal/win32"
)

// Process 是一次快照里的一条记录。
type Process struct {
	PID    uint32
	Parent uint32
	Cmd    string
}

// 快照缓存。
//
// **必须加锁**：UI 线程（托盘状态刷新）和多个 goroutine（Start/Stop）
// 会并发进来。无锁的读-改-写会让 slice 的底层指针或长度处于撕裂状态，
// 轻则判断错乱，重则调度器/运行时层面出问题。这是真实 data race。
var (
	snapshotMu  sync.Mutex
	snapshot    []Process
	snapshotSet bool
)

// Invalidate 让下一次 Snapshot 重新枚举。
func Invalidate() {
	snapshotMu.Lock()
	snapshot = nil
	snapshotSet = false
	snapshotMu.Unlock()
}

// Snapshot 枚举 node.exe 的 pid/父 pid/命令行（带缓存）。
//
// 拿不到命令行的进程（权限不足、已退出）直接跳过：
// 宁可漏判，也不能误判。
func Snapshot() []Process {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	if snapshotSet {
		return snapshot
	}
	var list []Process
	for _, pid := range win32.EnumNodePids() {
		cmd, err := win32.CommandLineOf(pid)
		if err != nil {
			continue
		}
		list = append(list, Process{PID: pid, Cmd: cmd})
	}
	snapshot = list
	snapshotSet = true
	return list
}

// Alive 判断进程是否还活着。
func Alive(pid uint32) bool {
	h, err := win32.OpenProcess(win32.ProcessQueryLimitedInformation, pid)
	if err != nil {
		return false
	}
	defer win32.CloseHandle(h)
	// 已退出的进程句柄会立即 signaled；WAIT_TIMEOUT(258) = 还活着
	const waitTimeout = 0
	return win32.WaitForSingleObject(h, waitTimeout) != 0
}

// Describe 把 pid 列表拼成给人看的字符串。
func Describe(pid uint32) string {
	return fmt.Sprintf("PID %d", pid)
}
