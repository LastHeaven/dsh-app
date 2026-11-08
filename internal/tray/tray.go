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
	"sync"
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
// **并发约定（改这个结构体之前先读完）：**
//
//   - hwnd / nid / icons / taskbarCreatedMsg：只在 UI 线程碰。
//     nid 受 nidMu 保护（UI 线程只用 TryLock，绝不阻塞等锁）。
//
//   - busy / exiting / updating：**会被多个 goroutine 同时读改写**，
//     所以一律用 atomic.Bool。尤其是 busy —— 菜单、管道转发、openUIAsync
//     三条路径都会调 startAsync，无保护时两个 goroutine 能同时通过
//     `if t.busy` 检查，各自跑一遍 Start()，两个 node 抢同一个端口。
//     这是真实存在的竞争，不是理论风险（-race 可复现）。
//
//   - url / upd / notifiedV / wasRunning：受 stateMu 保护。
//     用专门的 stateMu，不复用 nidMu —— nidMu 有"UI 线程绝不阻塞"的硬约定，
//     混进普通读写会把那条纪律破坏掉。
type App struct {
	opt   *config.Options
	hwnd  uintptr
	nid   notifyIconData
	icons *iconSet

	// TaskbarCreated 的消息 id：explorer 启动后会广播，收到就重建图标
	taskbarCreatedMsg uint32

	// 三个跨 goroutine 的布尔标志，用 atomic.Bool 无锁读写
	busy     atomicBool
	exiting  atomicBool
	updating atomicBool

	// 受 stateMu 保护的普通字段
	stateMu    sync.RWMutex
	url        string
	upd        updateState
	notifiedV  string // 已经弹过气泡的版本，按版本去重
	wasRunning bool

	updateStop chan struct{}
}

// atomicBool 是 atomic.Bool 的别名，让字段声明读起来短一些。
type atomicBool = atomic.Bool

// ---------------------------------------------------------------- 状态访问器
//
// 统一走这几个方法，避免调用方漏锁。busy 的三态语义（开始/结束/查询）
// 在外面直接操作容易写错，集中在这里更容易审查。

// markBusy 尝试进入"忙"状态；已经在忙则返回 false（调用方应直接放弃）。
//
// **必须用 CAS**：先查后置的写法在并发下不成立。
func (t *App) markBusy() bool {
	return t.busy.CompareAndSwap(false, true)
}

func (t *App) clearBusy()      { t.busy.Store(false) }
func (t *App) isBusy() bool    { return t.busy.Load() }
func (t *App) isExiting() bool { return t.exiting.Load() }

// beginExit 只在第一次调用时返回 true，用于保证退出流程只跑一遍。
func (t *App) beginExit() bool {
	return t.exiting.CompareAndSwap(false, true)
}

func (t *App) getURL() string {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.url
}

func (t *App) setURL(u string) {
	t.stateMu.Lock()
	t.url = u
	t.stateMu.Unlock()
}

func (t *App) getUpdateState() updateState {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.upd
}

func (t *App) setUpdateState(s updateState) {
	t.stateMu.Lock()
	t.upd = s
	t.stateMu.Unlock()
}

// noteNotified 记录"这个版本已经提示过"，并返回是否应当真的提示。
func (t *App) noteNotified(version string) bool {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.notifiedV == version {
		return false
	}
	t.notifiedV = version
	return true
}

func (t *App) getWasRunning() bool {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.wasRunning
}

func (t *App) setWasRunning(v bool) {
	t.stateMu.Lock()
	t.wasRunning = v
	t.stateMu.Unlock()
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
