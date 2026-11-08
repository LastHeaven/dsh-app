//go:build windows

package win32

import (
	"runtime"
	"unsafe"
)

func ShellNotifyIcon(action uint32, data *NotifyIconData) bool {
	r, _, _ := ProcShellNotifyIconW.Call(uintptr(action), uintptr(unsafe.Pointer(data)))
	return r != 0
}

// ShellExecuteOpen 用系统默认程序打开一个 URL/文件。
//
// 这是打开网页的**唯一正确姿势**：由 shell 按协议找到默认浏览器，
// 而且浏览器不会被本程序的管理员权限带成提权进程（提权的是我们，不是它）。
//
// 返回值 > 32 才算成功（ShellExecute 约定；<=32 是错误码）。
func ShellExecuteOpen(target string) bool {
	verb := UTF16Ptr("open")
	file := UTF16Ptr(target)
	if verb == nil || file == nil {
		return false
	}
	r, _, _ := ProcShellExecuteW.Call(
		0, // 无父窗口
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, 0, // 无参数、无工作目录
		SWShownormal)
	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	return r > 32
}
