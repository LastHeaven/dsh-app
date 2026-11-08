//go:build windows

package win32

import (
	"runtime"
	"unsafe"
)

func RegisterClassEx(wc *WndClassEx) uint16 {
	r, _, _ := ProcRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	return uint16(r)
}

// RegisterWindowMessage 注册/查询系统级消息 id。
//
// 用来拿 "TaskbarCreated" —— explorer 每次启动（含崩溃重启、开机后重新登录）
// 都会广播这条消息。托盘程序收到后必须重新 Shell_NotifyIcon(NIM_ADD)，
// 否则图标在通知区消失，用户点不到也就右键不出菜单。
func RegisterWindowMessage(name string) uint32 {
	p := UTF16Ptr(name)
	if p == nil {
		return 0
	}
	r, _, _ := ProcRegisterWindowMessage.Call(uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return uint32(r)
}

func CreateWindowEx(exStyle uint32, class, title uintptr, style uint32,
	x, y, w, h int32, parent, menu, instance uintptr) uintptr {
	r, _, _ := ProcCreateWindowExW.Call(
		uintptr(exStyle), class, title, uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, menu, instance, 0)
	return r
}

func DefWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	r, _, _ := ProcDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

func DestroyWindow(hwnd uintptr) {
	ProcDestroyWindow.Call(hwnd)
}

func GetMessage(msg *Msg, hwnd uintptr, min, max uint32) int32 {
	r, _, _ := ProcGetMessageW.Call(uintptr(unsafe.Pointer(msg)), hwnd, uintptr(min), uintptr(max))
	return int32(r)
}

func TranslateMessage(msg *Msg) {
	ProcTranslateMessage.Call(uintptr(unsafe.Pointer(msg)))
}

func DispatchMessage(msg *Msg) {
	ProcDispatchMessageW.Call(uintptr(unsafe.Pointer(msg)))
}

func PostQuitMessage(code int32) {
	ProcPostQuitMessage.Call(uintptr(code))
}

func PostMessage(hwnd uintptr, msg uint32, wparam, lparam uintptr) bool {
	r, _, _ := ProcPostMessageW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r != 0
}

func SetForegroundWindow(hwnd uintptr) bool {
	r, _, _ := ProcSetForegroundWindow.Call(hwnd)
	return r != 0
}

func GetCursorPos(p *Point) bool {
	r, _, _ := ProcGetCursorPos.Call(uintptr(unsafe.Pointer(p)))
	return r != 0
}

func SetProcessDpiAwarenessContext(ctx uintptr) {
	ProcSetProcessDpiAwareCtx.Call(ctx)
}

// SimulateAltKey 发一次"按下并松开 ALT"。
//
// 目的不是模拟按键本身，而是**解锁 SetForegroundWindow 的调用权限**：
// 系统只在"用户最近有过输入"时才允许普通进程抢占前台窗口。
// 托盘菜单即将弹出时发这个假按键，就能让紧随其后的 SetForegroundWindow 生效。
//
// MSDN 的 Shell_NotifyIcon 示例就是这么做的（发 WM_NULL 或模拟 ALT）。
// 不发的话，菜单会显示后立刻消失 —— 用户看到的就是"右键没反应"。
func SimulateAltKey() {
	const (
		inputKeyboard  = 1
		vkMenu         = 0x12 // VK_MENU (Alt)
		keyeventfKeyup = 0x0002
	)

	inputs := [2]InputStruct{
		{Type: inputKeyboard, Ki: KeybdInput{Vk: vkMenu}},
		{Type: inputKeyboard, Ki: KeybdInput{Vk: vkMenu, Flags: keyeventfKeyup}},
	}
	ProcSendInput.Call(2, uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(unsafe.Sizeof(inputs[0])))
}

func MessageBox(hwnd uintptr, text, caption string, flags uint32) int32 {
	// 指针不能裸奔进 Call，见 UTF16Ptr 的说明。
	tp, cp := UTF16Ptr(text), UTF16Ptr(caption)
	r, _, _ := ProcMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(tp)),
		uintptr(unsafe.Pointer(cp)),
		uintptr(flags))
	runtime.KeepAlive(tp)
	runtime.KeepAlive(cp)
	return int32(r)
}

// SetTimer 在指定窗口上挂一个定时器（消息投递到创建窗口的线程）。
func SetTimer(hwnd uintptr, id uint32, ms uint32) uintptr {
	r, _, _ := ProcSetTimer.Call(hwnd, uintptr(id), uintptr(ms), 0)
	return r
}
