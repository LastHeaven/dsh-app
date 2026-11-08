//go:build windows

// Package win32 是 Win32 API 的 P/Invoke 绑定，全项目唯一的系统调用出口。
//
// 全部走 syscall.NewLazyDLL，零第三方依赖：本机 Go 模块代理不可达
// （proxy.golang.org DNS 被拦），整条构建链必须零依赖。
//
// 文件划分：
//
//	dll.go       DLL 与 proc 句柄
//	const.go     常量（窗口消息、菜单、通知区、进程权限……）
//	struct.go    传给 Windows 的结构体（内存布局必须逐字节对得上）
//	util.go      UTF-16 与 last-error 之类的小工具
//	kernel.go    kernel32：进程、句柄、同步、控制台
//	user.go      user32：窗口、消息循环、置前
//	menu.go      user32：弹出菜单
//	icon.go      user32：图标加载与销毁
//	notify.go    shell32：通知区图标、ShellExecuteW
//	net.go       iphlpapi/ntdll/winhttp：TCP 表、命令行、网络
package win32

import "syscall"

var (
	MODKernel32 = syscall.NewLazyDLL("kernel32.dll")
	MODUser32   = syscall.NewLazyDLL("user32.dll")
	MODShell32  = syscall.NewLazyDLL("shell32.dll")
	MODNtdll    = syscall.NewLazyDLL("ntdll.dll")
	MODIphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	MODGdi32    = syscall.NewLazyDLL("gdi32.dll")
	MODWinhttp  = syscall.NewLazyDLL("winhttp.dll")
)

var (
	// kernel32
	ProcGetModuleHandleW     = MODKernel32.NewProc("GetModuleHandleW")
	ProcGetConsoleWindow     = MODKernel32.NewProc("GetConsoleWindow")
	ProcAttachConsole        = MODKernel32.NewProc("AttachConsole")
	ProcGetStdHandle         = MODKernel32.NewProc("GetStdHandle")
	ProcGetFileType          = MODKernel32.NewProc("GetFileType")
	ProcCreateMutexW         = MODKernel32.NewProc("CreateMutexW")
	ProcCloseHandle          = MODKernel32.NewProc("CloseHandle")
	ProcOpenProcess          = MODKernel32.NewProc("OpenProcess")
	ProcTerminateProcess     = MODKernel32.NewProc("TerminateProcess")
	ProcWaitForSingleObject  = MODKernel32.NewProc("WaitForSingleObject")
	ProcReadFile             = MODKernel32.NewProc("ReadFile")
	ProcCreateNamedPipeW     = MODKernel32.NewProc("CreateNamedPipeW")
	ProcConnectNamedPipe     = MODKernel32.NewProc("ConnectNamedPipe")
	ProcDisconnectNamedPipe  = MODKernel32.NewProc("DisconnectNamedPipe")
	ProcCreateFileW          = MODKernel32.NewProc("CreateFileW")
	ProcWriteFile            = MODKernel32.NewProc("WriteFile")
	ProcWriteConsoleW        = MODKernel32.NewProc("WriteConsoleW")
	ProcGetTickCount64       = MODKernel32.NewProc("GetTickCount64")
	ProcGetDiskFreeSpaceEx   = MODKernel32.NewProc("GetDiskFreeSpaceExW")
	ProcCreateToolhelp32Snap = MODKernel32.NewProc("CreateToolhelp32Snapshot")
	ProcProcess32FirstW      = MODKernel32.NewProc("Process32FirstW")
	ProcProcess32NextW       = MODKernel32.NewProc("Process32NextW")

	// user32
	ProcRegisterClassExW      = MODUser32.NewProc("RegisterClassExW")
	ProcRegisterWindowMessage = MODUser32.NewProc("RegisterWindowMessageW")
	ProcCreateWindowExW       = MODUser32.NewProc("CreateWindowExW")
	ProcDefWindowProcW        = MODUser32.NewProc("DefWindowProcW")
	ProcDestroyWindow         = MODUser32.NewProc("DestroyWindow")
	ProcGetMessageW           = MODUser32.NewProc("GetMessageW")
	ProcTranslateMessage      = MODUser32.NewProc("TranslateMessage")
	ProcDispatchMessageW      = MODUser32.NewProc("DispatchMessageW")
	ProcPostQuitMessage       = MODUser32.NewProc("PostQuitMessage")
	ProcPostMessageW          = MODUser32.NewProc("PostMessageW")
	ProcCreatePopupMenu       = MODUser32.NewProc("CreatePopupMenu")
	ProcAppendMenuW           = MODUser32.NewProc("AppendMenuW")
	ProcTrackPopupMenu        = MODUser32.NewProc("TrackPopupMenu")
	ProcDestroyMenu           = MODUser32.NewProc("DestroyMenu")
	ProcSetForegroundWindow   = MODUser32.NewProc("SetForegroundWindow")
	ProcSendInput             = MODUser32.NewProc("SendInput")
	ProcGetCursorPos          = MODUser32.NewProc("GetCursorPos")
	ProcDestroyIcon           = MODUser32.NewProc("DestroyIcon")
	ProcLoadImageW            = MODUser32.NewProc("LoadImageW")
	ProcMessageBoxW           = MODUser32.NewProc("MessageBoxW")
	ProcSetProcessDpiAwareCtx = MODUser32.NewProc("SetProcessDpiAwarenessContext")
	ProcSetTimer              = MODUser32.NewProc("SetTimer")

	// gdi32：取系统 DPI
	ProcGetDC         = MODUser32.NewProc("GetDC")
	ProcReleaseDC     = MODUser32.NewProc("ReleaseDC")
	ProcGetDeviceCaps = MODGdi32.NewProc("GetDeviceCaps")

	// shell32
	ProcShellNotifyIconW = MODShell32.NewProc("Shell_NotifyIconW")
	ProcShellExecuteW    = MODShell32.NewProc("ShellExecuteW")

	// ntdll：命令行（不走 WMI，也不需要 PEB+ReadProcessMemory）
	ProcNtQueryInformationProcess = MODNtdll.NewProc("NtQueryInformationProcess")

	// iphlpapi
	ProcGetExtendedTcpTable = MODIphlpapi.NewProc("GetExtendedTcpTable")

	// winhttp
	ProcWinHttpOpen        = MODWinhttp.NewProc("WinHttpOpen")
	ProcWinHttpConnect     = MODWinhttp.NewProc("WinHttpConnect")
	ProcWinHttpOpenRequest = MODWinhttp.NewProc("WinHttpOpenRequest")
	ProcWinHttpSetTimeouts = MODWinhttp.NewProc("WinHttpSetTimeouts")
	ProcWinHttpSendRequest = MODWinhttp.NewProc("WinHttpSendRequest")
	ProcWinHttpReceiveResp = MODWinhttp.NewProc("WinHttpReceiveResponse")
	ProcWinHttpQueryHdrs   = MODWinhttp.NewProc("WinHttpQueryHeaders")
	ProcWinHttpReadData    = MODWinhttp.NewProc("WinHttpReadData")
	ProcWinHttpCloseHandle = MODWinhttp.NewProc("WinHttpCloseHandle")
)
