//go:build windows

// 图标加载。
//
// 可靠做法：把 .ico 落到临时文件，用 LoadImageW 加载。图标数据本身仍然
// go:embed 编进 exe，运行时解出临时文件即可 —— 不依赖安装目录旁边
// 有没有 assets。
//
// **两套图标不能混**（见 AGENTS.md）：
//   - 托盘图标（本文件里这套）是透明背景，通知区浅色/深色主题下都看得清；
//   - exe 的图标是白色圆角卡片（build/app.ico），要贴在资源管理器里。
package tray

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/LastHeaven/dsh-app/internal/win32"
)

type iconSet struct {
	running uintptr
	stopped uintptr
	busy    uintptr
}

var (
	iconTempFiles []string
	iconTempMu    sync.Mutex
)

// extractIcon 把内嵌图标解到临时文件，返回路径。
//
// 文件名带内容哈希：不同版本/不同状态图标不会互相覆盖，也不会因为
// 上一次运行残留的半截文件而加载失败。
func extractIcon(name string) (string, error) {
	b, err := iconFS.ReadFile("icons/" + name)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(b)
	base := fmt.Sprintf("dsh-web-%s-%s.ico", strings.TrimSuffix(name, ".ico"), hex.EncodeToString(sum[:6]))
	path := filepath.Join(os.TempDir(), base)

	// 已有同内容文件就直接用
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(b)) {
		return path, nil
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}

	iconTempMu.Lock()
	iconTempFiles = append(iconTempFiles, path)
	iconTempMu.Unlock()
	return path, nil
}

// loadIcons 装载三态图标。任何一态失败就退回系统默认图标，
// 保证托盘一定能显示出来（宁可图标不对，也不能没有图标）。
func loadIcons() *iconSet {
	// 托盘尺寸随 DPI 走：100% 用 16，125%/150%/200% 用 20/24/32
	size := 16
	switch {
	case win32.SystemDPI() >= 192:
		size = 32
	case win32.SystemDPI() >= 144:
		size = 24
	case win32.SystemDPI() >= 120:
		size = 20
	}

	get := func(name string) uintptr {
		path, err := extractIcon(name)
		if err != nil {
			return 0
		}
		if h := win32.LoadIconFile(path, size); h != 0 {
			return h
		}
		return win32.LoadIconFile(path, 16)
	}

	s := &iconSet{
		running: get("running.ico"),
		stopped: get("stopped.ico"),
		busy:    get("busy.ico"),
	}

	// 兜底：用系统默认应用图标，确保至少有东西可画
	fallback := win32.LoadSystemIcon(32512) // IDI_APPLICATION
	if s.running == 0 {
		s.running = fallback
	}
	if s.stopped == 0 {
		s.stopped = fallback
	}
	if s.busy == 0 {
		s.busy = fallback
	}
	return s
}

func (s *iconSet) close() {
	// 系统共享图标不要销毁
	win32.DestroyIcon(s.running)
	win32.DestroyIcon(s.stopped)
	win32.DestroyIcon(s.busy)
}

// CleanupTemp 退出时清掉自己解出来的临时图标。
// 不删也能用（下次靠内容哈希复用），但别在 Temp 里堆垃圾。
func CleanupTemp() {
	iconTempMu.Lock()
	defer iconTempMu.Unlock()
	for _, p := range iconTempFiles {
		_ = os.Remove(p)
	}
	iconTempFiles = nil
}
