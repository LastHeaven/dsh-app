//go:build windows

// Package cli 是控制台输出与命令行参数解析。
//
// 只服务「跑完即退」的运维路径（-Help/-DryRun/-Stop/-CheckUpdate/-Update）
// 和进程启动时的参数解析；托盘不经过这里。
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/LastHeaven/dsh-app/internal/applog"
	"github.com/LastHeaven/dsh-app/internal/config"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

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
}

var con = &consoleOut{}

// Init 由 main 最先调用，早于任何输出。
func Init() {
	// windowsgui 进程没有自带控制台，附着到父进程（cmd / PowerShell）
	if win32.GetConsoleWindow() == 0 {
		win32.AttachConsole(0xFFFFFFFF) // ATTACH_PARENT_PROCESS = (DWORD)-1
	}

	h := win32.GetStdHandle(win32.StdOutputHandle)
	if h == 0 || h == win32.InvalidHandleValue {
		// 连句柄都没有：退化成只写日志文件
		return
	}
	if win32.GetFileType(h) == win32.FileTypeChar && win32.GetConsoleWindow() != 0 {
		// 真控制台：用 WriteConsoleW 输出 UTF-16
		con.useWriteConsole = true
		con.handle = h
	}
}

// Say 打印一行。控制台可能失效（-Stop 从计划任务调用时），所以同时写日志文件。
func Say(text string) { con.Say(text) }

// Say 打印一行。控制台可能失效（-Stop 从计划任务调用时），所以同时写日志文件。
func (c *consoleOut) Say(text string) {
	if c.useWriteConsole && c.handle != 0 {
		if win32.WriteConsole(c.handle, text+"\r\n") {
			applog.Headless(text)
			return
		}
		// 写控制台失败就退化成普通写法
		c.useWriteConsole = false
	}
	fmt.Fprintln(os.Stdout, text)
	applog.Headless(text)
}

// ---------------------------------------------------------------- 参数

// Options 是进程级配置，来自命令行。
type Options = config.Options

// ParseOptions 解析命令行参数。
func ParseOptions(args []string) *Options {
	o := config.Default()
	for i := 0; i < len(args); i++ {
		key := strings.ToLower(strings.TrimLeft(strings.TrimSpace(args[i]), "-/"))
		switch key {
		case "port":
			if i+1 < len(args) {
				i++
				if n, ok := Atoi(strings.TrimSpace(args[i])); ok {
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
		case "checkupdate":
			o.CheckUpdate = true
		case "noupdatecheck":
			o.NoUpdateCheck = true
		case "force":
			o.Force = true
		case "updateto", "update":
			// -UpdateTo 后面可选跟版本号；-Update 等同于升级到 latest
			if i+1 < len(args) {
				next := strings.TrimSpace(args[i+1])
				if !strings.HasPrefix(next, "-") {
					i++
					o.UpdateTo = next
				}
			}
		case "h", "help", "?":
			o.ShowHelp = true
		default:
			if strings.HasPrefix(key, "port:") {
				if n, ok := Atoi(key[5:]); ok {
					o.Port = n
				}
			}
			if strings.HasPrefix(key, "updateto:") {
				o.UpdateTo = strings.TrimSpace(args[i])[len("updateto:"):]
			}
		}
	}
	return o
}

// Atoi 只接受纯数字且限制在端口范围内。
func Atoi(s string) (int, bool) {
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

// PrintHelp 打印用法。
func PrintHelp() {
	con.Say(`dsh web 托盘版

  双击 dsh-web.exe          在右下角显示状态图标，并启动 dsh web
  dsh-web.exe -Stop         只关闭该端口上的 dsh web，不启动
  dsh-web.exe -DryRun       只检测并输出，不做任何改动
  dsh-web.exe -Port 8080    使用其它端口（停止也只影响该端口）
  dsh-web.exe -NoBrowser    启动后不自动打开浏览器
  dsh-web.exe -KeepExisting 不关闭已有实例（配合 -Port 调试用）
  dsh-web.exe -Headless     不显示托盘，按命令行跑完就退出（等同原 cmd 行为）

  dsh-web.exe -CheckUpdate  查一次最新版本就退出
  dsh-web.exe -Update       升级到最新版本（过程中服务会短暂停止）
  dsh-web.exe -UpdateTo 1.2.3  升级到指定版本
  dsh-web.exe -Force        允许重装同版本（修复损坏的安装）

托盘菜单：打开管理界面 / 启动 / 停止 / 重启 / 打开访问地址
          更新到新版本 / 检查更新 / 打开安装目录 / 查看日志 / 退出`)
}

// BoolStr 是给日志用的 True/False。
func BoolStr(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// Itoa 避免为了转一个整数把 strconv 拉进来。
func Itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// JoinComma 拼逗号分隔的列表。
func JoinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
