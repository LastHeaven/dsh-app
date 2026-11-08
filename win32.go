//go:build windows

// Win32 API 绑定。
//
// 全部走 syscall.NewLazyDLL，不引任何第三方包：
// 本机 Go 模块代理不可达（proxy.golang.org DNS 被拦），整条构建链必须零依赖。
package main

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	moduser32   = syscall.NewLazyDLL("user32.dll")
	modshell32  = syscall.NewLazyDLL("shell32.dll")
	modntdll    = syscall.NewLazyDLL("ntdll.dll")
	modiphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	modadvapi32 = syscall.NewLazyDLL("advapi32.dll")
	modgdi32    = syscall.NewLazyDLL("gdi32.dll")
)

var (
	// kernel32
	procGetModuleHandleW     = modkernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow     = modkernel32.NewProc("GetConsoleWindow")
	procAttachConsole        = modkernel32.NewProc("AttachConsole")
	procGetConsoleOutputCP   = modkernel32.NewProc("GetConsoleOutputCP")
	procGetStdHandle         = modkernel32.NewProc("GetStdHandle")
	procGetFileType          = modkernel32.NewProc("GetFileType")
	procCreateMutexW         = modkernel32.NewProc("CreateMutexW")
	procCloseHandle          = modkernel32.NewProc("CloseHandle")
	procOpenProcess          = modkernel32.NewProc("OpenProcess")
	procTerminateProcess     = modkernel32.NewProc("TerminateProcess")
	procWaitForSingleObject  = modkernel32.NewProc("WaitForSingleObject")
	procCreatePipe           = modkernel32.NewProc("CreatePipe")
	procSetHandleInformation = modkernel32.NewProc("SetHandleInformation")
	procCreateProcessW       = modkernel32.NewProc("CreateProcessW")
	procReadFile             = modkernel32.NewProc("ReadFile")
	procPeekNamedPipe        = modkernel32.NewProc("PeekNamedPipe")
	procGetExitCodeProcess   = modkernel32.NewProc("GetExitCodeProcess")
	procCreateNamedPipeW     = modkernel32.NewProc("CreateNamedPipeW")
	procConnectNamedPipe     = modkernel32.NewProc("ConnectNamedPipe")
	procDisconnectNamedPipe  = modkernel32.NewProc("DisconnectNamedPipe")
	procCreateFileW          = modkernel32.NewProc("CreateFileW")
	procWaitNamedPipeW       = modkernel32.NewProc("WaitNamedPipeW")
	procWriteFile            = modkernel32.NewProc("WriteFile")
	procSleep                = modkernel32.NewProc("Sleep")
	procLocalFree            = modkernel32.NewProc("LocalFree")
	procGetTickCount64       = modkernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceEx   = modkernel32.NewProc("GetDiskFreeSpaceExW")
	procCreateToolhelp32Snap = modkernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW      = modkernel32.NewProc("Process32FirstW")
	procProcess32NextW       = modkernel32.NewProc("Process32NextW")

	// user32
	procRegisterClassExW      = moduser32.NewProc("RegisterClassExW")
	procRegisterWindowMessage = moduser32.NewProc("RegisterWindowMessageW")
	procCreateWindowExW       = moduser32.NewProc("CreateWindowExW")
	procDefWindowProcW        = moduser32.NewProc("DefWindowProcW")
	procDestroyWindow         = moduser32.NewProc("DestroyWindow")
	procGetMessageW           = moduser32.NewProc("GetMessageW")
	procTranslateMessage      = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW      = moduser32.NewProc("DispatchMessageW")
	procPostQuitMessage       = moduser32.NewProc("PostQuitMessage")
	procPostMessageW          = moduser32.NewProc("PostMessageW")
	procCreatePopupMenu       = moduser32.NewProc("CreatePopupMenu")
	procAppendMenuW           = moduser32.NewProc("AppendMenuW")
	procTrackPopupMenu        = moduser32.NewProc("TrackPopupMenu")
	procDestroyMenu           = moduser32.NewProc("DestroyMenu")
	procSetForegroundWindow   = moduser32.NewProc("SetForegroundWindow")
	procGetForegroundWindow   = moduser32.NewProc("GetForegroundWindow")
	procSetFocus              = moduser32.NewProc("SetFocus")
	procSendInput             = moduser32.NewProc("SendInput")
	procGetCursorPos          = moduser32.NewProc("GetCursorPos")
	procCreateIconFromRes     = moduser32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon           = moduser32.NewProc("DestroyIcon")
	procLoadImageW            = moduser32.NewProc("LoadImageW")
	procMessageBoxW           = moduser32.NewProc("MessageBoxW")
	procGetDpiForWindow       = moduser32.NewProc("GetDpiForWindow")
	procSetProcessDpiAwareCtx = moduser32.NewProc("SetProcessDpiAwarenessContext")
	procSetTimer              = moduser32.NewProc("SetTimer")
	procKillTimer             = moduser32.NewProc("KillTimer")
	procWriteConsoleW         = modkernel32.NewProc("WriteConsoleW")
	procGetConsoleMode        = modkernel32.NewProc("GetConsoleMode")

	// gdi32：取系统 DPI
	procGetDC         = moduser32.NewProc("GetDC")
	procReleaseDC     = moduser32.NewProc("ReleaseDC")
	procGetDeviceCaps = modgdi32.NewProc("GetDeviceCaps")

	// shell32
	procShellNotifyIconW     = modshell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRt = modshell32.NewProc("Shell_NotifyIconGetRect")
	procShellExecuteW        = modshell32.NewProc("ShellExecuteW")

	// ntdll：命令行不走 WMI（AOT/原生下 System.Management 不可用，且这里本就没 .NET）
	procNtQueryInformationProcess = modntdll.NewProc("NtQueryInformationProcess")

	// iphlpapi
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")

	// advapi32：取不到 token 时读登录用户名
	procGetUserNameW = modadvapi32.NewProc("GetUserNameW")
)

// ------------------------------------------------------------------ 常量

const (
	// 进程访问权限
	processQueryLimitedInformation = 0x1000
	processTerminate               = 0x0001
	synchronize                    = 0x00100000

	// NtQueryInformationProcess 信息类
	processCommandLineInformation = 60

	// 窗口消息
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmContextMenu = 0x007B
	wmRButtonUp   = 0x0205
	wmLButtonUp   = 0x0202
	wmTimer       = 0x0113
	wmApp         = 0x8000
	wmNull        = 0x0000
	wmCommand     = 0x0111

	// 自定义消息：后台更新检查完成（WM_APP+1，避开菜单的 WM_APP 回调）
	wmUpdateReady = 0x8001

	// 菜单
	mfString       = 0x0000
	mfSeparator    = 0x0800
	mfGrayed       = 0x0001
	mfChecked      = 0x0008
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080

	// Shell_NotifyIcon
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	niifInfo    = 0x01
	niifWarning = 0x02
	niifError   = 0x03

	// 窗口样式
	wsPopup      = 0x80000000
	wsOverlapped = 0x00000000
	hwndMessage  = ^uintptr(2) // HWND_MESSAGE

	// 标准句柄
	stdOutputHandle = ^uintptr(11) // (DWORD)-11
	stdErrorHandle  = ^uintptr(12)
	stdInputHandle  = ^uintptr(10)

	// 文件类型
	fileTypePipe = 0x0003
	fileTypeChar = 0x0002

	// CreateProcess
	createNoWindow      = 0x08000000
	startfUseStdHandles = 0x00000100

	// DPI
	dpiAwarenessPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT -4

	// LoadImage
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
	lrShared       = 0x00008000

	// ShellExecute
	swShownormal = 1

	// MessageBox
	mbOK              = 0x00000000
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	// TCP 表
	afInet                  = 2
	tcpTableOwnerPidAll     = 5
	errorInsufficientBuffer = 122

	// 错误码
	errorAlreadyExists = 183

	// 命名管道
	//
	// 方向别搞反：服务端是**从客户端读命令**，所以服务端建 PIPE_ACCESS_INBOUND(1)；
	// 客户端写，用 GENERIC_WRITE。最初这里抄成了 PIPE_ACCESS_OUTBOUND(2)，
	// 服务端拿不到读权限，ConnectNamedPipe 之后读不出东西，转发静默失败、
	// 第二个实例既不退出也不提示。
	pipeAccessInbound  = 0x00000001
	openExisting       = 3
	genericWrite       = 0x40000000
	invalidHandleValue = ^uintptr(0)
)

// ------------------------------------------------------------------ 结构体

type point struct {
	X int32
	Y int32
}

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GuidItem         [16]byte
	BalloonIcon      uintptr
}

type startupInfoW struct {
	Cb            uint32
	Reserved      *uint16
	Desktop       *uint16
	Title         *uint16
	X             int32
	Y             int32
	XSize         int32
	YSize         int32
	XCountChars   int32
	YCountChars   int32
	FillAttribute uint32
	Flags         uint32
	ShowWindow    uint16
	CbReserved2   uint16
	Reserved2     *byte
	StdInput      uintptr
	StdOutput     uintptr
	StdError      uintptr
}

type processInformation struct {
	Process   uintptr
	Thread    uintptr
	ProcessID uint32
	ThreadID  uint32
}

type securityAttributes struct {
	Length             uint32
	SecurityDescriptor uintptr
	InheritHandle      bool
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type processCommandLineInfo struct {
	CommandLine unicodeString
}

// ------------------------------------------------------------------ 工具

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func utf16ToString(p *uint16, max int) string {
	if p == nil {
		return ""
	}
	var buf []uint16
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i*2)))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return string(littleEndianBytes(buf))
}

func littleEndianBytes(u []uint16) []rune {
	r := make([]rune, 0, len(u))
	for _, c := range u {
		r = append(r, rune(c))
	}
	return r
}

func lastErr() error {
	e := syscall.GetLastError()
	if e == nil {
		return nil
	}
	return e
}

// ------------------------------------------------------------------ kernel32

func getModuleHandle() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

func getConsoleWindow() uintptr {
	h, _, _ := procGetConsoleWindow.Call()
	return h
}

func attachConsole(pid uint32) bool {
	r, _, _ := procAttachConsole.Call(uintptr(pid))
	return r != 0
}

func getConsoleOutputCP() uint32 {
	r, _, _ := procGetConsoleOutputCP.Call()
	return uint32(r)
}

func getStdHandle(h uintptr) uintptr {
	r, _, _ := procGetStdHandle.Call(h)
	return r
}

func getFileType(h uintptr) uint32 {
	r, _, _ := procGetFileType.Call(h)
	return uint32(r)
}

// createMutexW 建互斥体。
//
// **返回的 err 必须来自这次 Call**：`GetLastError()` 在 Go 里是进程级状态，
// 中间任何一次 syscall 都可能把它盖掉（原实现就因此判断不出 ERROR_ALREADY_EXISTS，
// 导致单实例失效、出现两个托盘图标）。
// Call 的第三个返回值就是本次调用后的 last-error，直接用它。
func createMutexW(name string, initialOwner bool) (uintptr, bool, error) {
	owner := uintptr(0)
	if initialOwner {
		owner = 1
	}
	r, _, err := procCreateMutexW.Call(0, owner, uintptr(unsafe.Pointer(utf16Ptr(name))))
	if r == 0 {
		return 0, false, err
	}
	// ERROR_ALREADY_EXISTS(183) 表示同名互斥体已存在 —— 已有实例在跑。
	// 比较整数值而不是 syscall.Errno 类型：常量块里的无类型常量推导成 int，
	// 直接和 syscall.Errno 比会因为类型不同而永远不相等（原实现就栽在这里，
	// 表现为单实例完全失效、可以开出两个托盘图标）。
	already := false
	if en, ok := err.(syscall.Errno); ok && uintptr(en) == errorAlreadyExists {
		already = true
	}
	return r, already, nil
}

func closeHandle(h uintptr) {
	if h != 0 {
		procCloseHandle.Call(h)
	}
}

func openProcess(access uint32, pid uint32) (uintptr, error) {
	r, _, err := procOpenProcess.Call(uintptr(access), 0, uintptr(pid))
	if r == 0 {
		return 0, err
	}
	return r, nil
}

func terminateProcess(h uintptr, code uint32) bool {
	r, _, _ := procTerminateProcess.Call(h, uintptr(code))
	return r != 0
}

func waitForSingleObject(h uintptr, ms uint32) uint32 {
	r, _, _ := procWaitForSingleObject.Call(h, uintptr(ms))
	return uint32(r)
}

func sleep(ms uint32) {
	procSleep.Call(uintptr(ms))
}

func getTickCount64() uint64 {
	r, _, _ := procGetTickCount64.Call()
	return uint64(r)
}

// ------------------------------------------------------------------ user32

func registerClassEx(wc *wndClassEx) uint16 {
	r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	return uint16(r)
}

// registerWindowMessage 注册/查询系统级消息 id。
//
// 用来拿 "TaskbarCreated" —— explorer 每次启动（含崩溃重启、开机后重新登录）
// 都会广播这条消息。托盘程序收到后必须重新 Shell_NotifyIcon(NIM_ADD)，
// 否则图标在通知区消失，用户点不到也就右键不出菜单。
func registerWindowMessage(name string) uint32 {
	p := utf16Ptr(name)
	if p == nil {
		return 0
	}
	r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

func createWindowEx(exStyle uint32, class, title uintptr, style uint32,
	x, y, w, h int32, parent, menu, instance uintptr) uintptr {
	r, _, _ := procCreateWindowExW.Call(
		uintptr(exStyle), class, title, uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, menu, instance, 0)
	return r
}

func defWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

func destroyWindow(hwnd uintptr) {
	procDestroyWindow.Call(hwnd)
}

func getMessage(msg *msgStruct, hwnd uintptr, min, max uint32) int32 {
	r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(msg)), hwnd, uintptr(min), uintptr(max))
	return int32(r)
}

func translateMessage(msg *msgStruct) {
	procTranslateMessage.Call(uintptr(unsafe.Pointer(msg)))
}

func dispatchMessage(msg *msgStruct) {
	procDispatchMessageW.Call(uintptr(unsafe.Pointer(msg)))
}

func postQuitMessage(code int32) {
	procPostQuitMessage.Call(uintptr(code))
}

func postMessage(hwnd uintptr, msg uint32, wparam, lparam uintptr) bool {
	r, _, _ := procPostMessageW.Call(hwnd, uintptr(msg), wparam, lparam)
	return r != 0
}

func createPopupMenu() uintptr {
	r, _, _ := procCreatePopupMenu.Call()
	return r
}

func appendMenu(menu uintptr, flags uint32, id uintptr, text string) {
	procAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

func trackPopupMenu(menu uintptr, flags uint32, x, y int32, hwnd uintptr) uint32 {
	var rect [4]int32
	// 坐标是 int32，**必须按有符号值传入**：多显示器时副屏在左侧，
	// x 会是负数。直接 uintptr(x) 会把它变成 4294967295 之类的巨大正数，
	// 菜单会弹到屏幕外（或干脆不返回）。先转 int64 再转 uintptr 才安全。
	r, _, _ := procTrackPopupMenu.Call(menu, uintptr(flags),
		uintptr(int64(x)), uintptr(int64(y)), 0, hwnd,
		uintptr(unsafe.Pointer(&rect[0])))
	return uint32(r)
}

func destroyMenu(menu uintptr) {
	procDestroyMenu.Call(menu)
}

func setForegroundWindow(hwnd uintptr) bool {
	r, _, _ := procSetForegroundWindow.Call(hwnd)
	return r != 0
}

// INPUT 结构体的 x64 精确布局（**必须对，否则 SendInput 整批失败**）：
//
//	INPUT     = type(4) + pad(4) + union(32)  = 40 字节
//	KEYBDINPUT= wVk(2) wScan(2) dwFlags(4) time(4) dwExtraInfo(8) = 20 字节
//
// union 部分取最大成员 MOUSEINPUT(28) 对齐后的 32 字节。
// 最初把 keybdInput 补到 32、INPUT 变成 56 字节，SendInput 因 cbSize
// 不对直接返回 0 —— 模拟 ALT 完全无效，菜单还是弹不出来（实测踩到）。
type keybdInput struct {
	Vk    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	Extra uintptr
}

type inputStruct struct {
	Type uint32
	_    uint32
	Ki   keybdInput // 24 字节（20 内容 + 4 对齐填充）
	_    [8]byte    // 补齐 union 到 32 字节 → 整体 40 字节
}

// simulateAltKey 发一次"按下并松开 ALT"。
//
// 目的不是模拟按键本身，而是**解锁 SetForegroundWindow 的调用权限**：
// 系统只在"用户最近有过输入"时才允许普通进程抢占前台窗口。
// 托盘菜单即将弹出时发这个假按键，就能让紧随其后的 SetForegroundWindow 生效。
//
// MSDN 的 Shell_NotifyIcon 示例就是这么做的（发 WM_NULL 或模拟 ALT）。
// 不发的话，菜单会显示后立刻消失 —— 用户看到的就是"右键没反应"。
func simulateAltKey() {
	const (
		inputKeyboard  = 1
		vkMenu         = 0x12 // VK_MENU (Alt)
		keyeventfKeyup = 0x0002
	)

	inputs := [2]inputStruct{
		{Type: inputKeyboard, Ki: keybdInput{Vk: vkMenu}},
		{Type: inputKeyboard, Ki: keybdInput{Vk: vkMenu, Flags: keyeventfKeyup}},
	}
	procSendInput.Call(2, uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(unsafe.Sizeof(inputs[0])))
}

func getCursorPos(p *point) bool {
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(p)))
	return r != 0
}

func messageBox(hwnd uintptr, text, caption string, flags uint32) int32 {
	r, _, _ := procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(caption))),
		uintptr(flags))
	return int32(r)
}

func setProcessDpiAwarenessContext(ctx uintptr) {
	procSetProcessDpiAwareCtx.Call(ctx)
}

func getDpiForWindow(hwnd uintptr) uint32 {
	if procGetDpiForWindow.Find() != nil {
		return 96
	}
	r, _, _ := procGetDpiForWindow.Call(hwnd)
	return uint32(r)
}

// ------------------------------------------------------------------ 图标

// createIconFromResourceEx 直接从 ICO 里的 BMP 图块建 HICON，
// 不依赖文件系统（图标已 go:embed 进二进制）。
func createIconFromResourceEx(bits uintptr, size uint32, isIcon bool, w, h int32) uintptr {
	icon := uintptr(0)
	if isIcon {
		icon = 1
	}
	r, _, _ := procCreateIconFromRes.Call(bits, uintptr(size), icon,
		uintptr(w), uintptr(h), 0)
	return r
}

func destroyIcon(h uintptr) {
	if h != 0 {
		procDestroyIcon.Call(h)
	}
}

// ------------------------------------------------------------------ shell32

func shellNotifyIcon(action uint32, data *notifyIconData) bool {
	r, _, _ := procShellNotifyIconW.Call(uintptr(action), uintptr(unsafe.Pointer(data)))
	return r != 0
}

// shellExecuteOpen 用系统默认程序打开一个 URL/文件。
//
// 这是打开网页的**唯一正确姿势**：由 shell 按协议找到默认浏览器，
// 而且浏览器不会被本程序的管理员权限带成提权进程（提权的是我们，不是它）。
//
// 返回值 > 32 才算成功（ShellExecute 约定；<=32 是错误码）。
func shellExecuteOpen(target string) bool {
	verb := utf16Ptr("open")
	file := utf16Ptr(target)
	if verb == nil || file == nil {
		return false
	}
	r, _, _ := procShellExecuteW.Call(
		0, // 无父窗口
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, 0, // 无参数、无工作目录
		swShownormal) // SW_SHOWNORMAL
	return r > 32
}
