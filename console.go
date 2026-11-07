//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// ------------------------------------------------------------------ 控制台

// consoleOut 把中文正确送到父控制台。
//
// 坑：Go 在 Windows 上按 UTF-8 写 stdout，而 cmd/PowerShell 控制台默认是
// GBK(936) 之类的本地代码页，直接写会乱码。这里分两种情况：
//   - 输出被重定向到管道/文件（比如 `dsh-web.exe -DryRun | more`）：
//     就是普通 UTF-8 字节流，直接 os.Stdout 写。
//   - 直接写到真实控制台：走 WriteConsoleW 传 UTF-16，绕开代码页。
//
// windowsgui 子系统的进程默认没有控制台，所以要先 AttachConsole 到父进程。
type consoleOut struct {
	useWriteConsole bool
	handle          uintptr
	logFallback     string // 控制台不可用时的文件兜底
}

var con = &consoleOut{}

// initConsole 由 main 最先调用，早于任何输出。
func initConsole() {
	// windowsgui 进程没有自带控制台，附着到父进程（cmd / PowerShell）
	if getConsoleWindow() == 0 {
		attachConsole(0xFFFFFFFF) // ATTACH_PARENT_PROCESS = (DWORD)-1
	}

	h := getStdHandle(stdOutputHandle)
	if h == 0 || h == invalidHandleValue {
		// 连句柄都没有：退化成只写日志文件
		return
	}
	if getFileType(h) == fileTypeChar && getConsoleWindow() != 0 {
		// 真控制台：用 WriteConsoleW 输出 UTF-16
		con.useWriteConsole = true
		con.handle = h
	}
}

// Say 打印一行。控制台可能失效（-Stop 从计划任务调用时），所以同时写日志文件。
func (c *consoleOut) Say(text string) {
	if c.useWriteConsole && c.handle != 0 {
		if writeConsole(c.handle, text+"\r\n") {
			c.log(text)
			return
		}
		// 写控制台失败就退化成普通写法
		c.useWriteConsole = false
	}
	fmt.Fprintln(os.Stdout, text)
	c.log(text)
}

// WriteStdout 处理被重定向的情况：直接 UTF-8 字节流
func (c *consoleOut) writeRaw(s string) {
	if c.useWriteConsole && c.handle != 0 {
		if writeConsole(c.handle, s) {
			return
		}
		c.useWriteConsole = false
	}
	_, _ = os.Stdout.WriteString(s)
}

func writeConsole(h uintptr, s string) bool {
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return true
	}
	var written uint32
	r, _, _ := procWriteConsoleW.Call(h,
		uintptr(unsafe.Pointer(&u[0])),
		uintptr(len(u)),
		uintptr(unsafe.Pointer(&written)), 0)
	return r != 0
}

// log 关键信息同时落文件，便于排查（原来 Headless.Say 就是这么做的）
func (c *consoleOut) log(text string) {
	if c.logFallback == "" {
		c.logFallback = Paths.Root()
	}
	f, err := os.OpenFile(joinPath(c.logFallback, "headless.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(timestamp() + "  " + text + "\r\n")
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	if strings.HasSuffix(dir, `\`) || strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + `\` + name
}

// ------------------------------------------------------------------ 参数

type Options struct {
	Port         int
	NoBrowser    bool
	KeepExisting bool
	StopOnly     bool
	DryRun       bool
	Headless     bool
	ShowHelp     bool
}

func parseOptions(args []string) *Options {
	o := &Options{Port: 3080}
	for i := 0; i < len(args); i++ {
		key := strings.ToLower(strings.TrimLeft(strings.TrimSpace(args[i]), "-/"))
		switch key {
		case "port":
			if i+1 < len(args) {
				i++
				if n, ok := atoi(strings.TrimSpace(args[i])); ok {
					o.Port = n
				}
			}
		case "nobrowser":
			o.NoBrowser = true
		case "keepexisting":
			o.KeepExisting = true
		case "stop":
			o.StopOnly = true
		case "dryrun":
			o.DryRun = true
		case "headless":
			o.Headless = true
		case "h", "help", "?":
			o.ShowHelp = true
		default:
			if strings.HasPrefix(key, "port:") {
				if n, ok := atoi(key[5:]); ok {
					o.Port = n
				}
			}
		}
	}
	return o
}

func atoi(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0, false
		}
	}
	return n, true
}

func printHelp() {
	text := `dsh web 托盘版

  双击 dsh-web.exe          在右下角显示状态图标，并启动 dsh web
  dsh-web.exe -Stop         只关闭该端口上的 dsh web，不启动
  dsh-web.exe -DryRun       只检测并输出，不做任何改动
  dsh-web.exe -Port 8080    使用其它端口（停止也只影响该端口）
  dsh-web.exe -NoBrowser    启动后不自动打开浏览器
  dsh-web.exe -KeepExisting 不关闭已有实例（配合 -Port 调试用）
  dsh-web.exe -Headless     不显示托盘，按命令行跑完就退出（等同原 cmd 行为）

托盘菜单：打开管理界面 / 启动 / 停止 / 重启 / 打开访问地址
         打开安装目录 / 查看日志 / 退出`
	con.Say(text)
}

func timestamp() string {
	t := timeNow()
	return fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
}
