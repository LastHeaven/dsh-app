//go:build windows

// Package tray 是通知区图标本体。
//
// 约束（改动前请读 docs/PITFALLS.md）：
//   - 窗口、定时器、消息队列都有**线程亲和性**，全部在 main 里
//     runtime.LockOSThread 的那条线程上创建和销毁；
//   - wndProc 里不能做耗时操作，否则整个托盘"点了没反应"；
//   - NOTIFYICONDATAW 会被多线程改动，必须走 nidMu（且 UI 线程只用 TryLock）。
package tray

import (
	"embed"
	"os"
	"sync/atomic"
	"time"

	"github.com/LastHeaven/dsh-app/internal/applog"
	"github.com/LastHeaven/dsh-app/internal/config"
)

//go:embed icons/running.ico icons/stopped.ico icons/busy.ico
var iconFS embed.FS

const (
	title          = "dsh web"
	className      = "DshWebTrayWnd"
	trayID         = 1
	timerPoll      = 1
	timerIconCheck = 2

	pollIntervalMs = 3000

	// 图标健康检查间隔。
	//
	// 有了 TaskbarCreated 之后，explorer 重启是即时恢复的，这个定时器只是
	// 兜底（通知区"隐藏不常用图标"、系统从休眠恢复等场景不会发广播）。
	// 10 秒是个折中：够快让用户几乎无感，又不会太频繁地打扰 shell。
	iconCheckIntervalMs = 10000
)

// 菜单命令 id
const (
	cmdOpenUI = 1001 + iota
	cmdStart
	cmdStop
	cmdRestart
	cmdOpenURL
	cmdOpenDir
	cmdOpenLogs
	cmdExit
	cmdCheckUpdate
	cmdApplyUpdate
)

// App 是托盘的全部状态。
//
// 除了 hwnd/nid（只能在 UI 线程碰），其余字段的并发约定写在各自注释里。
type App struct {
	opt        *config.Options
	hwnd       uintptr
	nid        notifyIconData
	icons      *iconSet
	url        string
	busy       bool
	exiting    bool
	wasRunning bool

	// TaskbarCreated 的消息 id：explorer 启动后会广播，收到就重建图标
	taskbarCreatedMsg uint32

	// 更新状态。只在这个结构体里存，UI 线程和后台 goroutine
	// 通过 PostMessage(WMUpdateReady) 交接，避免并发读写界面状态。
	upd        updateState
	updating   bool
	notifiedV  string // 已经弹过气泡的版本，按版本去重
	updateStop chan struct{}
}

var current *App

// New 创建一个托盘实例（只装载图标，不建窗口）。
func New(opt *config.Options) *App {
	return &App{opt: opt, icons: loadIcons()}
}

// Diag 写托盘诊断日志。
func Diag(format string, a ...interface{}) { applog.Diag(format, a...) }

func timeNow() time.Time { return time.Now() }

// debugMsgs 为真时把每条窗口消息写进诊断日志，用于定位卡死点。
// 由环境变量 DSH_TRAY_DEBUG=1 开启，平时零开销。
var debugMsgs = os.Getenv("DSH_TRAY_DEBUG") == "1"

// uiHeartbeat 由 UI 线程定期递增，看门狗 goroutine 检查它是否停滞。
//
// **为什么需要它**：托盘主线程一旦卡死，用户看到的就是"右键没反应"，
// 但进程还活着、图标还在，从外部完全看不出卡在哪。有了心跳+日志，
// 就能在日志里直接看到"UI 线程从某一刻起不再响应"以及当时它在做什么。
var uiHeartbeat int64

func beat() { atomic.AddInt64(&uiHeartbeat, 1) }
