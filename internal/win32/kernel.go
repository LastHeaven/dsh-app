//go:build windows

package win32

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// ---------------------------------------------------------------- 控制台

func GetModuleHandle() uintptr {
	h, _, _ := ProcGetModuleHandleW.Call(0)
	return h
}

func GetConsoleWindow() uintptr {
	h, _, _ := ProcGetConsoleWindow.Call()
	return h
}

func AttachConsole(pid uint32) bool {
	r, _, _ := ProcAttachConsole.Call(uintptr(pid))
	return r != 0
}

func GetStdHandle(h uintptr) uintptr {
	r, _, _ := ProcGetStdHandle.Call(h)
	return r
}

func GetFileType(h uintptr) uint32 {
	r, _, _ := ProcGetFileType.Call(h)
	return uint32(r)
}

// WriteConsole 走 WriteConsoleW 传 UTF-16，绕开控制台代码页。
func WriteConsole(h uintptr, s string) bool {
	p := UTF16Ptr(s)
	if p == nil {
		return false
	}
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	var written uint32
	r, _, _ := ProcWriteConsoleW.Call(h, uintptr(unsafe.Pointer(p)),
		uintptr(n), uintptr(unsafe.Pointer(&written)), 0)
	runtime.KeepAlive(p)
	return r != 0
}

// ---------------------------------------------------------------- 进程与句柄

// CreateMutex 建互斥体。
//
// **返回的 err 必须来自这次 Call**：GetLastError() 在 Go 里是进程级状态，
// 中间任何一次 syscall 都可能把它盖掉（早期版本因此判断不出
// ERROR_ALREADY_EXISTS，导致单实例失效、出现两个托盘图标）。
func CreateMutex(name string, initialOwner bool) (uintptr, bool, error) {
	owner := uintptr(0)
	if initialOwner {
		owner = 1
	}
	np := UTF16Ptr(name)
	if np == nil {
		return 0, false, errors.New("bad mutex name")
	}
	r, _, err := ProcCreateMutexW.Call(0, owner, uintptr(unsafe.Pointer(np)))
	runtime.KeepAlive(np)
	if r == 0 {
		return 0, false, err
	}
	// ERROR_ALREADY_EXISTS(183) 表示同名互斥体已存在 —— 已有实例在跑。
	// 比较整数值而不是 syscall.Errno 类型：常量块里的无类型常量推导成 int，
	// 直接和 syscall.Errno 比会因为类型不同而永远不相等。
	already := false
	if en, ok := err.(syscall.Errno); ok && uintptr(en) == ErrorAlreadyExists {
		already = true
	}
	return r, already, nil
}

func CloseHandle(h uintptr) {
	if h != 0 {
		ProcCloseHandle.Call(h)
	}
}

func OpenProcess(access uint32, pid uint32) (uintptr, error) {
	r, _, err := ProcOpenProcess.Call(uintptr(access), 0, uintptr(pid))
	if r == 0 {
		return 0, err
	}
	return r, nil
}

func TerminateProcess(h uintptr, code uint32) bool {
	r, _, _ := ProcTerminateProcess.Call(h, uintptr(code))
	return r != 0
}

func WaitForSingleObject(h uintptr, ms uint32) uint32 {
	r, _, _ := ProcWaitForSingleObject.Call(h, uintptr(ms))
	return uint32(r)
}

func GetTickCount64() uint64 {
	r, _, _ := ProcGetTickCount64.Call()
	return uint64(r)
}

// EnumNodePids 用 Toolhelp 快照枚举 node.exe 的 pid。
func EnumNodePids() []uint32 {
	const th32csSnapProcess = 0x00000002
	snap, _, _ := ProcCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snap == InvalidHandleValue || snap == 0 {
		return nil
	}
	defer CloseHandle(snap)

	var e ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	r, _, _ := ProcProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&e)))
	if r == 0 {
		return nil
	}
	var out []uint32
	for {
		if equalFoldUTF16(e.ExeFile[:], "node.exe") {
			out = append(out, e.ProcessID)
		}
		e.Size = uint32(unsafe.Sizeof(e))
		r, _, _ = ProcProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&e)))
		if r == 0 {
			break
		}
	}
	return out
}

func equalFoldUTF16(a []uint16, b string) bool {
	if len(a) < len(b) {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := a[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		d := b[i]
		if d >= 'A' && d <= 'Z' {
			d += 'a' - 'A'
		}
		if c != uint16(d) {
			return false
		}
	}
	return a[len(b)] == 0
}

// CommandLineOf 读指定进程的完整命令行。
//
// 用 NtQueryInformationProcess(class 60)。**不要再用 WMI**：它依赖反射，
// 原生编译/裁剪下直接崩（MissingMethodException: WbemDefPath）。
// 也不要退回读 PEB（NtQueryInformationProcess+ReadProcessMemory）——
// 那条在 node.exe 上返回 ERROR_ACCESS_DENIED(5)，已证明走不通。
func CommandLineOf(pid uint32) (string, error) {
	h, err := OpenProcess(ProcessQueryLimitedInformation, pid)
	if err != nil {
		return "", err
	}
	defer CloseHandle(h)

	// 先探长度，再分配
	var needed uint32
	ProcNtQueryInformationProcess.Call(h, uintptr(ProcessCommandLineInformation),
		0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 {
		return "", fmt.Errorf("no command line for pid %d", pid)
	}

	buf := make([]byte, needed+16)
	var got uint32
	status, _, _ := ProcNtQueryInformationProcess.Call(h, uintptr(ProcessCommandLineInformation),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&got)))
	if status != 0 {
		return "", fmt.Errorf("NtQueryInformationProcess status 0x%x", status)
	}

	length := *(*uint16)(unsafe.Pointer(&buf[0]))
	if length == 0 {
		return "", fmt.Errorf("empty command line for pid %d", pid)
	}
	ptr := *(*uintptr)(unsafe.Pointer(&buf[8]))
	if ptr == 0 {
		return "", fmt.Errorf("null command line buffer for pid %d", pid)
	}
	n := int(length) / 2
	if n <= 0 || n > 64*1024 {
		return "", fmt.Errorf("implausible command line length %d for pid %d", n, pid)
	}
	// 内核把命令行拷进了我们自己的 buf（Buffer 指向 buf 内部），
	// 所以直接从 buf 里按偏移读即可 —— 完全不需要把 uintptr 转回指针，
	// 也就不会触发 go vet 的 possible misuse of unsafe.Pointer。
	off := int(ptr - uintptr(unsafe.Pointer(&buf[0])))
	if off < 0 || off+n*2 > len(buf) {
		return "", fmt.Errorf("command line buffer out of range for pid %d", pid)
	}
	raw := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), n)
	return syscall.UTF16ToString(raw), nil
}

// DiskFreeBytes 返回路径所在磁盘的可用字节数。
func DiskFreeBytes(path string) (uint64, error) {
	p := UTF16Ptr(path)
	if p == nil {
		return 0, errors.New("bad path")
	}
	var free, total, avail uint64
	r, _, err := ProcGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)))
	runtime.KeepAlive(p)
	if r == 0 {
		return 0, err
	}
	return avail, nil
}
