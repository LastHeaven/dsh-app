//go:build windows

// 图标加载。
//
// 试过 CreateIconFromResourceEx 直接从内存里的 ICO 图块建 HICON：
// **全部返回 0 且不设置 last-error**（各帧、各参数组合、isIcon/isCursor 都试过），
// 在这台机器上就是不可用，别在这条路上浪费时间。
//
// 可靠做法（C# 版也是这么做的）：把 .ico 落到临时文件，用 LoadImageW 加载。
// 图标数据本身仍然 go:embed 编进 exe，运行时解出临时文件即可 —— 不依赖
// 安装目录旁边有没有 assets。
package main

import (
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

//go:embed assets/running.ico assets/stopped.ico assets/busy.ico
var iconFS embed.FS

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
	b, err := iconFS.ReadFile("assets/" + name)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(b)
	base := fmt.Sprintf("dsh-web-%s-%s.ico", trimExt(name), hex.EncodeToString(sum[:6]))
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

func trimExt(s string) string {
	if i := len(s) - 4; i > 0 && s[i] == '.' {
		return s[:i]
	}
	return s
}

// loadIconFile 用 LoadImageW 从 .ico 文件建 HICON。
// 先按目标尺寸取，失败再退回系统默认尺寸。
func loadIconFile(path string, size int) uintptr {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	if size > 0 {
		h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)),
			imageIcon, uintptr(size), uintptr(size), lrLoadFromFile)
		if h != 0 {
			return h
		}
	}
	h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)),
		imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	return h
}

// loadIcons 装载三态图标。任何一态失败就退回系统默认图标，
// 保证托盘一定能显示出来（宁可图标不对，也不能没有图标）。
func loadIcons() *iconSet {
	// 托盘尺寸随 DPI 走：100% 用 16，125%/150%/200% 用 20/24/32
	size := 16
	switch {
	case systemDPI() >= 192:
		size = 32
	case systemDPI() >= 144:
		size = 24
	case systemDPI() >= 120:
		size = 20
	}

	get := func(name string) uintptr {
		path, err := extractIcon(name)
		if err != nil {
			return 0
		}
		if h := loadIconFile(path, size); h != 0 {
			return h
		}
		return loadIconFile(path, 16)
	}

	s := &iconSet{
		running: get("running.ico"),
		stopped: get("stopped.ico"),
		busy:    get("busy.ico"),
	}

	// 兜底：用系统默认应用图标，确保至少有东西可画
	fallback := loadSystemIcon(32512) // IDI_APPLICATION
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

func loadSystemIcon(id int) uintptr {
	h, _, _ := procLoadImageW.Call(0, uintptr(id), imageIcon, 0, 0, lrShared)
	return h
}

func (s *iconSet) close() {
	// 系统共享图标不要销毁
	destroyIcon(s.running)
	destroyIcon(s.stopped)
	destroyIcon(s.busy)
}

// cleanupIconTemp 退出时清掉自己解出来的临时图标。
// 不删也能用（下次靠内容哈希复用），但别在 Temp 里堆垃圾。
func cleanupIconTemp() {
	iconTempMu.Lock()
	defer iconTempMu.Unlock()
	for _, p := range iconTempFiles {
		_ = os.Remove(p)
	}
	iconTempFiles = nil
}

// systemDPI 取系统 DPI，用来决定托盘图标取哪一档尺寸。
func systemDPI() uint32 {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 96
	}
	defer procReleaseDC.Call(0, hdc)
	// LOGPIXELSX = 88
	dpi, _, _ := procGetDeviceCaps.Call(hdc, 88)
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}
