//go:build windows

// dsh web 托盘版 —— 程序入口。
//
// 常驻右下角通知区，用鲸鱼图标表示状态：
//
//	蓝色 = 运行中   琥珀色 = 启动中/异常   灰色 = 未运行
//
// 左键点击 = 打开管理界面；右键 = 启动 / 停止 / 重启 / 打开目录 / 退出。
//
// 命令行参数（供脚本、计划任务调用，跑完即退，不显示托盘）：
//
//	-Port 8080  -NoBrowser  -KeepExisting  -Stop  -DryRun  -Headless  -Help
//	-CheckUpdate  -Update  -UpdateTo <ver>  -Force  -NoUpdateCheck
//
// 这个文件只做「分流」：把命令行判成四条互斥的路径，然后交给 internal 包。
// 具体实现都在 internal/ 下，见 docs/ARCHITECTURE.md。
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/LastHeaven/dsh-app/internal/applog"
	"github.com/LastHeaven/dsh-app/internal/cli"
	"github.com/LastHeaven/dsh-app/internal/config"
	"github.com/LastHeaven/dsh-app/internal/service"
	"github.com/LastHeaven/dsh-app/internal/single"
	"github.com/LastHeaven/dsh-app/internal/tray"
	"github.com/LastHeaven/dsh-app/internal/updater"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

func main() {
	// 必须在任何输出之前接管父控制台，否则中文乱码
	cli.Init()

	args := os.Args[1:]
	opt := cli.ParseOptions(args)

	if opt.ShowHelp {
		cli.PrintHelp()
		return
	}

	// 更新相关的运维动作，也是跑完就退，不构造托盘。
	if opt.CheckUpdate {
		os.Exit(runCheckUpdate())
	}
	if opt.UpdateTo != "" || isUpdateRequested(args) {
		os.Exit(runUpdateCommand(opt))
	}

	// 运维动作：跑完就退，**绝不构造托盘**。
	// 早先版本在无界面模式下也 new TrayApp(...)，导致短暂闪出托盘图标、
	// 甚至走错启动分支。
	if opt.DryRun || opt.StopOnly || opt.Headless {
		os.Exit(runHeadless(opt))
	}

	// 单实例：已有托盘在跑就把命令转交给它，避免出现第二个图标
	isPrimary, mutexErr := single.Acquire()
	applog.Diag("single-instance: primary=%v err=%v pid=%d", isPrimary, mutexErr, os.Getpid())
	if !isPrimary {
		single.Forward(args)
		applog.Diag("forwarded, exiting")
		return
	}

	runTray(opt)
}

// runTray 是托盘路径。单独拆出来是为了让 main 保持"分流器"的形状。
func runTray(opt *config.Options) {
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
	// GetMessage（正常的空闲等待），看门狗只能看到"心跳不涨"。
	//
	// LockOSThread 之后 main goroutine 终身绑定一条线程，窗口/定时器/消息
	// 循环都在同一条线程上，问题消失。
	runtime.LockOSThread()

	win32.SetProcessDpiAwarenessContext(win32.DPIAwarenessPerMonitorV2)

	app := tray.New(opt)

	// 后台收转交过来的命令（Headless 路径不会走到这里）
	go func() {
		for line := range single.Commands() {
			tray.HandleForwarded(line)
		}
	}()

	defer tray.CleanupTemp()
	app.Run()

	// Run() 只在消息循环结束时返回。**必须在这里让进程真正退出**：
	// 后台还有管道监听、更新检查等 goroutine 阻塞着，它们会让进程继续
	// 存活，形成一个"没有窗口的托盘空壳" —— 通知区图标看着还在，
	// 但点击送不到窗口，表现就是右键菜单永远打不开。
	//
	// 实测过：进程存活、11 个线程、HandleCount 393，但 EnumWindows
	// 查这个 PID 的顶层窗口数是 0。
	applog.Diag("Run() 返回，消息循环已结束，退出进程")
	os.Exit(0)
}

// runHeadless 跑完即退的三条路径：DryRun / Stop / 启动。
func runHeadless(opt *config.Options) int {
	cli.Say("[启动] 参数 Port=" + cli.Itoa(opt.Port) +
		" DryRun=" + cli.BoolStr(opt.DryRun) + " Stop=" + cli.BoolStr(opt.StopOnly) +
		" NoBrowser=" + cli.BoolStr(opt.NoBrowser) + " KeepExisting=" + cli.BoolStr(opt.KeepExisting))

	if opt.DryRun {
		live := service.Live()
		if len(live) > 0 {
			parts := make([]string, 0, len(live))
			for _, p := range live {
				parts = append(parts, "PID "+cli.Itoa(int(p.PID)))
			}
			cli.Say("检测到 dsh web：" + cli.JoinComma(parts))
		} else {
			cli.Say("未检测到已运行的 dsh web。")
		}
		return 0
	}

	if opt.StopOnly {
		n := service.Stop(opt.Port)
		if n > 0 {
			cli.Say("dsh web 已关闭（端口 " + cli.Itoa(opt.Port) + "，结束 " + cli.Itoa(n) + " 个进程）。")
		} else {
			cli.Say("端口 " + cli.Itoa(opt.Port) + " 上没有运行中的 dsh web。")
		}
		return 0
	}

	url, err := service.Start(opt)
	if err != nil {
		cli.Say("启动失败：" + err.Error())
		return 1
	}
	cli.Say("dsh web 已启动：" + url)
	return 0
}

// runCheckUpdate 查一次最新版就退出（脚本/排障用）。
func runCheckUpdate() int {
	root := updater.InstallRoot()
	if root == "" {
		cli.Say("未找到 dsh 安装目录，请确认已执行：npm i -g @deepseek-ai/dsh")
		return 1
	}
	cli.Say("安装目录：" + root)
	cli.Say("当前版本：" + updater.LocalVersion(root))

	st := updater.Check()
	if st.Err != "" {
		cli.Say("检查失败：" + st.Err)
		return 1
	}
	cli.Say("最新版本：" + st.Latest)
	if st.HasUpdate {
		cli.Say("有新版本可用：" + st.Current + " → " + st.Latest)
	} else {
		cli.Say("已是最新版本。")
	}
	return 0
}

// runUpdateCommand 执行升级（-Update / -UpdateTo）。
//
// **注意这里会短暂停止服务**，所以必须提示清楚，不能静默跑。
func runUpdateCommand(opt *config.Options) int {
	target := opt.UpdateTo
	if target == "" {
		st := updater.Check()
		if st.Err != "" {
			cli.Say("检查更新失败：" + st.Err)
			return 1
		}
		if !st.HasUpdate {
			cli.Say("已是最新版本（" + st.Current + "），无需升级。")
			return 0
		}
		target = st.Latest
	}

	cli.Say("准备升级到 " + target + "（过程中服务会短暂停止）")

	// 命令行路径要显式打日志到控制台，托盘路径由 tray 自己显示
	logf := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		cli.Say(msg)
		updater.Log("%s", msg)
	}

	// 命令行升级不自动开浏览器（升级完服务已在跑，用户自己开）
	upOpt := *opt
	upOpt.NoBrowser = true

	if err := updater.Run(target, &upOpt, logf); err != nil {
		cli.Say("升级失败：" + err.Error())
		updater.Log("升级失败：%v", err)
		return 1
	}
	cli.Say("升级完成：" + target)
	updater.Log("升级完成：%s", target)
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
