//go:build windows

package win32

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// UTF16Ptr 把字符串转成以 NUL 结尾的 UTF-16 缓冲，失败返回 nil。
//
// **调用方有义务保活。** 直接把返回值塞进 Proc.Call 是未定义行为：
//
//	Proc.Call(..., uintptr(unsafe.Pointer(win32.UTF16Ptr(s))))
//
// 那个 *uint16 没有任何 Go 变量持有，只有 uintptr 形式的地址进了 syscall。
// GC 只扫描栈上的**指针**，uintptr 不算指针 —— Call 期间一旦发生 GC，
// 缓冲就被回收，被调方拿到野指针。这个坑在本项目里真实出现过：
// 表现是托盘菜单"时灵时不灵"，因为坏内存有时刚好还是原内容。
//
// 正确写法：
//
//	p := win32.UTF16Ptr(s)
//	r, _, _ := ...Call(uintptr(unsafe.Pointer(p)))
//	runtime.KeepAlive(p)
func UTF16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// UTF16ToString 读 NUL 结尾的 UTF-16 缓冲，最多 max 个字符。
func UTF16ToString(p *uint16, max int) string {
	if p == nil {
		return ""
	}
	buf := make([]uint16, 0, max)
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + uintptr(i*2)))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return string(utf16.Decode(buf))
}

// LastErr 取当前线程的 last-error。
//
// 优先用 Proc.Call 的第三个返回值：syscall.GetLastError() 是**进程级**状态，
// 中间任何一次调用都可能覆盖它（早期单实例判定失效就是这个原因）。
func LastErr() error {
	return syscall.GetLastError()
}
