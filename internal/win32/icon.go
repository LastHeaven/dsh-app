//go:build windows

package win32

import (
	"runtime"
	"unsafe"
)

// LoadIconFile 用 LoadImageW 从 .ico 文件建 HICON。
// 先按目标尺寸取，失败再退回系统默认尺寸。
//
// 试过 CreateIconFromResourceEx 直接从内存里的 ICO 图块建 HICON：
// **全部返回 0 且不设置 last-error**（各帧、各参数组合都试过），
// 在这台机器上就是不可用，别在这条路上浪费时间。
func LoadIconFile(path string, size int) uintptr {
	p := UTF16Ptr(path)
	if p == nil {
		return 0
	}
	if size > 0 {
		h, _, _ := ProcLoadImageW.Call(0, uintptr(unsafe.Pointer(p)),
			ImageIcon, uintptr(size), uintptr(size), LRLoadFromFile)
		if h != 0 {
			return h
		}
	}
	h, _, _ := ProcLoadImageW.Call(0, uintptr(unsafe.Pointer(p)),
		ImageIcon, 0, 0, LRLoadFromFile|LRDefaultSize)
	runtime.KeepAlive(p)
	return h
}

// LoadSystemIcon 取系统图标（IDI_APPLICATION = 32512），永不失败。
func LoadSystemIcon(id int) uintptr {
	h, _, _ := ProcLoadImageW.Call(0, uintptr(id), ImageIcon, 0, 0, LRShared)
	return h
}

func DestroyIcon(h uintptr) {
	if h != 0 {
		ProcDestroyIcon.Call(h)
	}
}

// SystemDPI 取系统 DPI，用来决定托盘图标取哪一档尺寸。
func SystemDPI() uint32 {
	hdc, _, _ := ProcGetDC.Call(0)
	if hdc == 0 {
		return 96
	}
	defer ProcReleaseDC.Call(0, hdc)
	// LOGPIXELSX = 88
	dpi, _, _ := ProcGetDeviceCaps.Call(hdc, 88)
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}
