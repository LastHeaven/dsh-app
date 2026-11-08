//go:build windows

// Package single 保证只有一个托盘实例。
//
// 第二个实例不会建第二个图标，而是通过命名管道把命令交给第一个实例。
// 互斥体名和原先 C# 版保持一致，避免新旧版本同时跑时出现两个托盘。
package single

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/LastHeaven/dsh-app/internal/win32"
)

const (
	mutexName = `Global\dsh-web-tray-{7E2F5C41-9B3A-4E52-8C1D-2A6F0B4D9E13}`
	pipeName  = `\\.\pipe\dsh-web-tray-{7E2F5C41-9B3A-4E52-8C1D-2A6F0B4D9E13}`
)

var (
	mutexHandle uintptr
	commandCh   = make(chan string, 4)
	pipeOnce    sync.Once
)

// Commands 是转交过来的命令行（只有主实例会收到）。
func Commands() <-chan string { return commandCh }

// Acquire 尝试成为唯一实例。
//
// 返回 true 表示本进程就是主实例（已启动管道监听）；
// 返回 false 表示已有实例在跑。
func Acquire() (bool, error) {
	h, already, err := win32.CreateMutex(mutexName, true)
	if err != nil {
		// 互斥体建不出来（权限等）就当作主实例，宁可多一个图标也不要起不来
		startPipeListener()
		return true, nil
	}
	if already {
		// 已有实例，句柄立即释放
		win32.CloseHandle(h)
		return false, nil
	}
	mutexHandle = h // 存活到进程结束
	startPipeListener()
	return true, nil
}

// Forward 把命令行交给已运行的实例。失败则弹个提示框。
func Forward(args []string) {
	payload := strings.Join(args, " ") + "\r\n"
	deadline := time.Now().Add(2500 * time.Millisecond)

	for time.Now().Before(deadline) {
		h := createFile(pipeName, win32.GenericWrite, 0, win32.OpenExisting)
		if h == win32.InvalidHandleValue || h == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		ok := writeHandle(h, payload)
		win32.CloseHandle(h)
		if ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	win32.MessageBox(0, "dsh web 已在托盘中运行，请查看右下角通知区域。", "dsh web",
		win32.MBOK|win32.MBIconInformation|win32.MBSetForeground)
}

// startPipeListener 后台循环收命令。
//
// 注意：**不能**在 Headless 路径里调用 —— 那是跑完即退的，不该常驻监听。
func startPipeListener() {
	pipeOnce.Do(func() {
		go func() {
			for {
				line, err := serveOneConnection()
				if err != nil {
					time.Sleep(200 * time.Millisecond)
					continue
				}
				select {
				case commandCh <- line:
				default: // 队列满就丢弃，托盘那边总会做一次状态刷新
				}
			}
		}()
	})
}

// serveOneConnection 建一次实例、等一个客户端、读一行。
func serveOneConnection() (string, error) {
	sa := win32.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(win32.SecurityAttributes{})),
		InheritHandle: false,
	}
	namePtr := win32.UTF16Ptr(pipeName)
	if namePtr == nil {
		return "", fmt.Errorf("bad pipe name")
	}

	h, _, err := win32.ProcCreateNamedPipeW.Call(
		uintptr(unsafe.Pointer(namePtr)),
		win32.PipeAccessInbound, // 服务端读：客户端写进来的命令
		0,                       // 字节流模式
		1,                       // 同时 1 个实例
		4096, 4096,              // 出/入缓冲
		0, // 默认超时
		uintptr(unsafe.Pointer(&sa)))
	if h == win32.InvalidHandleValue || h == 0 {
		return "", err
	}
	defer win32.CloseHandle(h)

	// ConnectNamedPipe：客户端已连上时返回 ERROR_PIPE_CONNECTED
	r, _, _ := win32.ProcConnectNamedPipe.Call(h, 0)
	if r == 0 {
		if le, ok := win32.LastErr().(syscall.Errno); !ok || uintptr(le) != win32.ErrorPipeConnected {
			return "", fmt.Errorf("connect failed")
		}
	}

	var line []byte
	buf := make([]byte, 1)
	for i := 0; i < 4096; i++ {
		var read uint32
		ok, _, _ := win32.ProcReadFile.Call(h,
			uintptr(unsafe.Pointer(&buf[0])), 1,
			uintptr(unsafe.Pointer(&read)), 0)
		if ok == 0 || read == 0 {
			break
		}
		if buf[0] == '\n' {
			break
		}
		if buf[0] != '\r' {
			line = append(line, buf[0])
		}
	}
	win32.ProcDisconnectNamedPipe.Call(h)
	return strings.TrimSpace(string(line)), nil
}

func createFile(name string, access uint32, shareMode uint32, disposition uint32) uintptr {
	p := win32.UTF16Ptr(name)
	if p == nil {
		return win32.InvalidHandleValue
	}
	h, _, _ := win32.ProcCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(access),
		uintptr(shareMode),
		0,
		uintptr(disposition),
		0,
		0)
	return h
}

func writeHandle(h uintptr, s string) bool {
	b := []byte(s)
	if len(b) == 0 {
		return true
	}
	var written uint32
	r, _, _ := win32.ProcWriteFile.Call(h,
		uintptr(unsafe.Pointer(&b[0])),
		uintptr(len(b)),
		uintptr(unsafe.Pointer(&written)), 0)
	return r != 0
}
