//go:build windows

// dsh web 托盘版 —— Go 重写版。
//
// 常驻右下角通知区，用鲸鱼图标表示状态：
//
//	黑色 = 运行中   琥珀色 = 启动中/异常   灰色 = 未运行
//
// 左键点击 = 打开管理界面；右键 = 启动 / 停止 / 重启 / 打开目录 / 退出。
//
// 命令行参数（供脚本、计划任务调用，跑完即退，不显示托盘）：
//
//	-Port 8080  -NoBrowser  -KeepExisting  -Stop  -DryRun  -Headless  -Help
package main

import (
	"fmt"
	"os"
)

func main() {
	// 必须在任何输出之前接管父控制台，否则中文乱码
	initConsole()

	opt := parseOptions(os.Args[1:])

	if opt.ShowHelp {
		printHelp()
		return
	}

	// 运维动作：跑完就退，**绝不构造托盘**。
	// 早先版本在无界面模式下也 new TrayApp(...)，导致短暂闪出托盘图标、
	// 甚至走错启动分支。
	if opt.DryRun || opt.StopOnly || opt.Headless {
		os.Exit(runHeadless(opt))
	}

	// 单实例：已有托盘在跑就把命令转交给它，避免出现第二个图标
	isPrimary, mutexErr := acquireSingleInstance()
	diagLog("single-instance: primary=%v err=%v pid=%d", isPrimary, mutexErr, os.Getpid())
	if !isPrimary {
		forwardCommand(os.Args[1:])
		diagLog("forwarded, exiting")
		return
	}

	setProcessDpiAwarenessContext(dpiAwarenessPerMonitorV2)

	app := newTrayApp(opt)
	tray = app

	// 后台收转交过来的命令（Headless 路径不会走到这里）
	go func() {
		for line := range commandCh {
			handleForwarded(line)
		}
	}()

	defer cleanupIconTemp()
	app.Run()
}

// runHeadless 跑完即退的三条路径：DryRun / Stop / 启动。
func runHeadless(opt *Options) int {
	con.Say("[启动] 参数 Port=" + itoa(opt.Port) +
		" DryRun=" + boolStr(opt.DryRun) + " Stop=" + boolStr(opt.StopOnly) +
		" NoBrowser=" + boolStr(opt.NoBrowser) + " KeepExisting=" + boolStr(opt.KeepExisting))

	if opt.DryRun {
		live := liveWeb()
		if len(live) > 0 {
			parts := make([]string, 0, len(live))
			for _, p := range live {
				parts = append(parts, "PID "+itoa(int(p.PID)))
			}
			con.Say("检测到 dsh web：" + joinComma(parts))
		} else {
			con.Say("未检测到已运行的 dsh web。")
		}
		return 0
	}

	if opt.StopOnly {
		n := Stop(opt.Port)
		if n > 0 {
			con.Say("dsh web 已关闭（端口 " + itoa(opt.Port) + "，结束 " + itoa(n) + " 个进程）。")
		} else {
			con.Say("端口 " + itoa(opt.Port) + " 上没有运行中的 dsh web。")
		}
		return 0
	}

	url, err := Start(opt)
	if err != nil {
		con.Say("启动失败：" + err.Error())
		return 1
	}
	con.Say("dsh web 已启动：" + url)
	return 0
}

// diagLog 写诊断日志（托盘没有控制台，问题只能靠文件排查）。
func diagLog(format string, a ...interface{}) {
	f, err := os.OpenFile(joinPath(Paths.Root(), "tray-diag.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(timestamp() + "  " + fmt.Sprintf(format, a...) + "\r\n")
}

func boolStr(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func itoa(n int) string {
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

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
