//go:build windows

package win32

import (
	"runtime"
	"unsafe"
)

func CreatePopupMenu() uintptr {
	r, _, _ := ProcCreatePopupMenu.Call()
	return r
}

// AppendMenu 往菜单里加一项。
//
// 两个坑，都实测踩过：
//
// **1. 分隔符的 lpNewItem 必须是 NULL。**
// 对 MF_SEPARATOR 项，AppendMenuW 不读文本参数。传一个空字符串指针进去
// （UTF16Ptr("") 得到的是 "\0" 的地址，不是 NULL），AppendMenuW 会试图
// 把它当文本处理，行为未定义 —— 实测第二项（第一个分隔符）直接卡死，
// 整个消息循环停摆，托盘从此不响应。
//
// **2. 指针不能裸奔进 Call。** 见 UTF16Ptr 的说明。
func AppendMenu(menu uintptr, flags uint32, id uintptr, text string) {
	if flags&MFSeparator != 0 || text == "" {
		ProcAppendMenuW.Call(menu, uintptr(flags), id, 0)
		return
	}
	p := UTF16Ptr(text)
	if p == nil {
		return
	}
	ProcAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func TrackPopupMenu(menu uintptr, flags uint32, x, y int32, hwnd uintptr) uint32 {
	var rect [4]int32
	// 坐标是 int32，**必须按有符号值传入**：多显示器时副屏在左侧，
	// x 会是负数。直接 uintptr(x) 会把它变成 4294967295 之类的巨大正数，
	// 菜单会弹到屏幕外（或干脆不返回）。先转 int64 再转 uintptr 才安全。
	r, _, _ := ProcTrackPopupMenu.Call(menu, uintptr(flags),
		uintptr(int64(x)), uintptr(int64(y)), 0, hwnd,
		uintptr(unsafe.Pointer(&rect[0])))
	return uint32(r)
}

func DestroyMenu(menu uintptr) {
	ProcDestroyMenu.Call(menu)
}
