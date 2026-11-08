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
	"runtime"
	"strings"
)

func main() {
	// 必须在任何输出之前接管父控制台，否则中文乱码
	initConsole()

	opt := parseOptions(os.Args[1:])

	if opt.ShowHelp {
		printHelp()
		return
	}

	// 更新相关的运维动作，也是跑完就退，不构造托盘。
	if opt.CheckUpdate {
		os.Exit(runCheckUpdate())
	}
	if opt.UpdateTo != "" || isUpdateRequested(os.Args[1:]) {
		os.Exit(runUpdateCommand(opt))
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

	// **必须锁定 OS 线程，这是托盘的命门。**
	//
	// Win32 的窗口、定时器、消息队列全都绑定在**创建它的那个 OS 线程**上：
	// CreateWindowExW 建窗口时记录了当时的线程，SetTimer 也把定时器挂到
	// 那个线程的消息队列，GetMessage 只能取到**本线程**的消息。
	//
	// 而 Go 的 goroutine 会在 OS 线程之间自由迁移 —— 同一个 goroutine
	// 这次跑在线程 A、下次可能跑到线程 B。一旦 CreateWindow/SetTimer 之后
	// goroutine 被挪到别的线程，GetMessage 就在错误的线程上等消息，
	// 结果就是：窗口建好了、图标也在、但 WM_TIMER 和托盘回调全部收不到，
	// 表现成"托盘点了没反应、定时刷新也停了"。
	//
	// 实测症状很有欺骗性：进程 Responding 为 True、goroutine 栈显示卡在
	// getMessage（正常的空闲等待），看门狗只能看到"心跳不涨"。
	//
	// LockOSThread 之后 main goroutine 终身绑定一条线程，窗口/定时器/消息
	// 循环都在同一条线程上，问题消失。
	runtime.LockOSThread()

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

	// Run() 只在消息循环结束时返回。**必须在这里让进程真正退出**：
	// 后台还有管道监听、更新检查等 goroutine 阻塞着，它们会让进程继续
	// 存活，形成一个"没有窗口的托盘空壳" —— 通知区图标看着还在，
	// 但点击送不到窗口，表现就是右键菜单永远打不开。
	//
	// 实测过：进程存活、11 个线程、HandleCount 393，但 EnumWindows
	// 查这个 PID 的顶层窗口数是 0。
	diagLog("Run() 返回，消息循环已结束，退出进程")
	os.Exit(0)
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

// runCheckUpdate 查一次最新版就退出（脚本/排障用）。
func runCheckUpdate() int {
	root := dshInstallRoot()
	if root == "" {
		con.Say("未找到 dsh 安装目录，请确认已执行：npm i -g @deepseek-ai/dsh")
		return 1
	}
	con.Say("安装目录：" + root)
	con.Say("当前版本：" + localDshVersion(root))

	st := checkUpdate()
	if st.Err != "" {
		con.Say("检查失败：" + st.Err)
		return 1
	}
	con.Say("最新版本：" + st.Latest)
	if st.HasUpdate {
		con.Say("有新版本可用：" + st.Current + " → " + st.Latest)
	} else {
		con.Say("已是最新版本。")
	}
	return 0
}

// runUpdateCommand 执行升级（-Update / -UpdateTo）。
//
// **注意这里会短暂停止服务**，所以必须提示清楚，不能静默跑。
func runUpdateCommand(opt *Options) int {
	target := opt.UpdateTo
	if target == "" {
		st := checkUpdate()
		if st.Err != "" {
			con.Say("检查更新失败：" + st.Err)
			return 1
		}
		if !st.HasUpdate {
			con.Say("已是最新版本（" + st.Current + "），无需升级。")
			return 0
		}
		target = st.Latest
	}

	con.Say("准备升级到 " + target + "（过程中服务会短暂停止）")

	// 命令行路径要显式打日志到控制台，托盘路径由 tray 自己显示
	logf := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		con.Say(msg)
		updateLog("%s", msg)
	}

	// 命令行升级不自动开浏览器（升级完服务已在跑，用户自己开）
	upOpt := *opt
	upOpt.NoBrowser = true

	if err := runUpdate(target, &upOpt, logf); err != nil {
		con.Say("升级失败：" + err.Error())
		updateLog("升级失败：%v", err)
		return 1
	}
	con.Say("升级完成：" + target)
	updateLog("升级完成：%s", target)
	return 0
}

// isUpdateRequested 识别裸 -Update（不带版本号）。
func isUpdateRequested(args []string) bool {
	for _, a := range args {
		k := strings.ToLower(strings.TrimLeft(strings.TrimSpace(a), "-/"))
		if k == "update" || k == "updateto" {
			return true
		}
	}
	return false
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
