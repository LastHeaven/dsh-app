//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// ------------------------------------------------------------------ 时间

func timeNow() time.Time { return time.Now() }

// ------------------------------------------------------------------ 托盘

const (
	trayTitle      = "dsh web"
	trayClassName  = "DshWebTrayWnd"
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
	cmdSeparator = 0
)

type TrayApp struct {
	opt        *Options
	hwnd       uintptr
	nid        notifyIconData
	icons      *iconSet
	url        string
	busy       bool
	exiting    bool
	wasRunning bool
	menuFont   uintptr

	// TaskbarCreated 的消息 id：explorer 启动后会广播，收到就重建图标
	taskbarCreatedMsg uint32

	// 更新状态。只在这个结构体里存，UI 线程和后台 goroutine
	// 通过 postMessage(wmUpdateReady) 交接，避免并发读写界面状态。
	upd        updateState
	updating   bool
	notifiedV  string // 已经弹过气泡的版本，按版本去重
	updateStop chan struct{}
}

var tray *TrayApp

func newTrayApp(opt *Options) *TrayApp {
	return &TrayApp{opt: opt, icons: loadIcons()}
}

// Run 建隐藏消息窗口、装托盘图标、跑消息循环。
func (t *TrayApp) Run() {
	// 只收消息、永不显示的窗口
	instance := getModuleHandle()
	className := utf16Ptr(trayClassName)

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  instance,
		ClassName: className,
	}
	if registerClassEx(&wc) == 0 {
		// 类已注册也继续（重复运行不会到这儿，但别硬失败）
		_ = 0
	}

	// 窗口用**不可见的普通顶层窗口**，不是 HWND_MESSAGE。
	//
	// 为什么不用 HWND_MESSAGE：它是"仅消息窗口"，收不到广播消息，
	// 而 explorer 重启时发的 TaskbarCreated 正是广播（HWND_BROADCAST）。
	// 实测确认：用 HWND_MESSAGE 时整条 explorer 重启恢复链路是死的，
	// 只能靠 30 秒一次的自检兜底，中间那段时间右键是坏的。
	//
	// 普通顶层窗口 + WS_POPUP + 不 ShowWindow，用户看不见，但能收到广播。
	t.hwnd = createWindowEx(0, uintptr(unsafe.Pointer(className)), 0,
		wsPopup, 0, 0, 0, 0, 0, 0, instance)
	if t.hwnd == 0 {
		messageBox(0, "无法创建托盘窗口：%s", trayTitle, mbOK|mbIconInformation)
		return
	}

	t.addIcon()
	t.refreshState()

	// explorer 重启（崩溃恢复、开机后重登录）会广播 TaskbarCreated。
	// 不处理的话图标从此消失、点击也送不到窗口 —— 这是"用久了右键菜单
	// 打不开"最主要的成因之一。
	//
	// **必须注册在创建窗口之后、消息循环之前**：注册只是拿到消息 id，
	// 顺序其实不敏感，但放在这里更清楚 —— 紧接着就是消息循环。
	t.taskbarCreatedMsg = registerWindowMessage("TaskbarCreated")
	diagLog("TaskbarCreated msg id = 0x%X", t.taskbarCreatedMsg)

	// 轮询用 SetTimer，跟着消息循环走，不额外开线程。
	// **必须在这里设**：hwnd 是 Run() 内部创建的，放到 main 里会拿到 0。
	procSetTimer.Call(t.hwnd, timerPoll, pollIntervalMs, 0)

	// 图标健康检查用另一个定时器，频率低一些（30 秒）。
	// 光靠 TaskbarCreated 不够：某些情况下 explorer 不广播、或者图标
	// 被通知区"隐藏"后丢失，只能靠主动探测发现。
	procSetTimer.Call(t.hwnd, timerIconCheck, iconCheckIntervalMs, 0)

	// 首次启动：本端口的服务没在跑就拉起来
	if !Paths.IsPortListening(t.opt.Port) {
		go t.startAsync()
	}

	// UI 线程看门狗。托盘卡死时用户只能看到"右键没反应"，
	// 从外部看不出卡在哪；有心跳日志就能直接定位。
	t.startWatchdog()

	// 后台更新检查。必须延后启动：启动瞬间正在加载管理界面，
	// 这会儿发 HTTPS 请求会和首屏抢资源，也容易被误当成启动卡顿。
	if !t.opt.NoUpdateCheck {
		t.updateStop = make(chan struct{})
		go t.updateLoop()
	}

	t.messageLoop()
}

// updateLoop 后台周期检查更新。退出时通过 updateStop 收掉。
func (t *TrayApp) updateLoop() {
	select {
	case <-time.After(updateInitialDelay):
	case <-t.updateStop:
		return
	}

	for {
		if t.exiting {
			return
		}
		st := checkUpdate()
		if st.Err != "" {
			diagLog("checkUpdate: %s", st.Err)
			updateLog("检查更新失败：%s", st.Err)
		} else {
			updateLog("检查更新：当前 %s，最新 %s，有更新=%v",
				st.Current, st.Latest, st.HasUpdate)
		}
		t.upd = st
		// 回窗口线程：菜单和气泡都必须由创建窗口的线程改
		postMessage(t.hwnd, wmUpdateReady, 0, 0)

		select {
		case <-time.After(updateInterval):
		case <-t.updateStop:
			return
		}
	}
}

// onUpdateReady 在 UI 线程处理检查结果（由 wmUpdateReady 触发）。
func (t *TrayApp) onUpdateReady() {
	if t.exiting || !t.upd.HasUpdate {
		return
	}
	// 同一个版本只提示一次，别每 6 小时骚扰
	if t.notifiedV == t.upd.Latest {
		return
	}
	t.notifiedV = t.upd.Latest
	t.balloon(trayTitle,
		fmt.Sprintf("dsh 有新版本 %s，右键图标可更新。", t.upd.Latest),
		niifInfo)
}

func (t *TrayApp) addIcon() {
	t.nid = notifyIconData{
		Wnd:             t.hwnd,
		ID:              trayID,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmApp,
		Icon:            t.icons.stopped,
	}
	t.nid.Size = uint32(unsafe.Sizeof(t.nid))
	copyUTF16(t.nid.Tip[:], trayTitle)
	ok := shellNotifyIcon(nimAdd, &t.nid)
	diagLog("addIcon: ok=%v hwnd=%d icon=%d running=%d stopped=%d busy=%d",
		ok, t.hwnd, t.nid.Icon, t.icons.running, t.icons.stopped, t.icons.busy)
}

func copyUTF16(dst []uint16, s string) {
	u := utf16.Encode([]rune(s))
	n := len(u)
	if n > len(dst)-1 {
		n = len(dst) - 1
	}
	for i := 0; i < n; i++ {
		dst[i] = u[i]
	}
	dst[n] = 0
}

// reAddIcon 重新注册图标。
//
// 用于两种场景：
//  1. 收到 TaskbarCreated（explorer 重启了，之前的 NIM_ADD 全失效）；
//  2. 自检发现图标已不在通知区。
//
// **先 NIM_DELETE 再 NIM_ADD**：图标还在时直接 ADD 会失败(E_FAIL)。
//
// 判定成功与否只看 NIM_ADD 的返回值 —— 不要再用 GetRect 复核：
// 这台机器上 TrayNotifyWnd 不存在，GetRect 恒失败，会把成功的 ADD
// 误判成失败（实测踩过，导致每 10 秒重建一次的死循环）。
func (t *TrayApp) reAddIcon(reason string) bool {
	if t.exiting || t.hwnd == 0 {
		return false
	}

	// 本函数既可能被 UI 线程调（TaskbarCreated、checkIconAlive），
	// 也可能被 goroutine 调。**UI 线程上不能阻塞等锁** ——
	// 这里统一用 TryLock，拿不到就交给下一轮：
	// TaskbarCreated 有 10 秒一次的自检兜底，晚一点重建没关系，
	// 但让 UI 线程卡住等锁会直接导致"整个托盘点不动"。
	if !nidMu.TryLock() {
		diagLog("reAddIcon: reason=%s 拿不到锁，跳过本轮", reason)
		return false
	}

	shellNotifyIcon(nimDelete, &t.nid)

	// 重建时把 nid 恢复到"完整状态"：图标 + 提示文字 + 回调消息
	// 缺任何一个，图标会显示但不响应点击。
	t.nid.Flags = nifMessage | nifIcon | nifTip
	t.nid.CallbackMessage = wmApp
	if t.nid.Icon == 0 {
		t.nid.Icon = t.icons.stopped
	}

	ok := shellNotifyIcon(nimAdd, &t.nid)
	nidMu.Unlock()

	diagLog("reAddIcon: reason=%s ok=%v hwnd=%d", reason, ok, t.hwnd)
	if ok {
		// 真注册上了，把状态刷回去（图标/提示文字）
		t.refreshState()
	}
	return ok
}

// checkIconAlive 定时自检图标是否还在。
//
// **不能用 Shell_NotifyIconGetRect 判断**：实测这台机器上
// `FindWindow("TrayNotifyWnd")` 始终返回 0（通知区不是独立顶层窗口），
// GetRect 因此恒定返回 E_FAIL —— 拿它做判据会导致"每 10 秒误判丢失、
// 反复重建"的死循环。
//
// 改用 NIM_MODIFY 的返回值：图标若已从 shell 注销，MODIFY 会返回 FALSE。
// 这是唯一不依赖 GetRect、又能真实反映注册状态的信号。
//
// **本函数跑在 UI 线程上，绝对不能阻塞等锁**：nidMu 可能正被 goroutine
// 里的 setIcon/setText 长时间持有（startAsync 启动服务要 8 秒，
// 期间会多次 setIcon/setText）。UI 线程一等，消息循环立刻停摆，
// 用户看到的就是"右键点不动、左键也没反应"。
// 所以这里用 TryLock：拿不到就直接跳过这一轮，10 秒后再试即可。
func (t *TrayApp) checkIconAlive() {
	if t.exiting || t.hwnd == 0 {
		return
	}
	if !nidMu.TryLock() {
		// 拿不到锁说明有 goroutine 正在改状态，跳过这轮
		return
	}
	f := uint32(nifMessage | nifIcon | nifTip)
	t.nid.Flags = f
	ok := shellNotifyIcon(nimModify, &t.nid)
	nidMu.Unlock()
	if ok {
		return // 图标还在，一切正常
	}
	diagLog("checkIconAlive: NIM_MODIFY 失败，图标已失效，尝试重建")
	if !t.reAddIcon("自检发现图标失效") {
		diagLog("checkIconAlive: 重建后备仍报错，等下一轮（shell 可能还没就绪）")
	}
}

func (t *TrayApp) messageLoop() {
	var msg msgStruct
	for {
		r := getMessage(&msg, 0, 0, 0)
		if r == 0 {
			// 正常的 WM_QUIT：窗口已销毁，可以退出
			diagLog("messageLoop: WM_QUIT")
			return
		}
		if r == -1 {
			// **这是个真实踩到的坑**：getMessage 返回 -1 表示出错（不是退出）。
			// 原实现直接 break 出循环，于是 Run() 返回、main 返回，但进程里
			// 还有其他 goroutine（管道监听、更新检查）让它继续活着 ——
			// 结果就是一个"窗口没了、进程还在"的空壳：通知区图标看着还在，
			// 但点击事件送不到窗口，右键菜单永远打不开。
			//
			// 现在明确记日志，并且让整个进程退出，避免留下空壳。
			diagLog("messageLoop: GetMessage 返回 -1（错误），退出进程以免留下无窗口的托盘空壳")
			return
		}
		translateMessage(&msg)
		dispatchMessage(&msg)
	}
}

// wndProc 是回调，**不能**做耗时操作（会卡住整个托盘）。

func wndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	if tray == nil {
		return defWindowProc(hwnd, msg, wparam, lparam)
	}

	// 每次进回调先打心跳：看门狗据此判断 UI 线程是否还活着。
	atomic.AddInt64(&uiHeartbeat, 1)

	// 调试模式：把每条消息都记进日志，用于定位卡死点。
	// 用环境变量开启，平时零开销。
	if debugMsgs {
		diagLog("wndProc: msg=0x%04X wparam=0x%X lparam=0x%X", msg, wparam, lparam)
	}

	switch msg {
	case wmApp:
		switch uint32(lparam) & 0xFFFF {
		case wmLButtonUp:
			go tray.openUIAsync()
		case wmRButtonUp:
			tray.showMenu()
		}
		return 0

	case wmTimer:
		// **必须按 wparam 区分定时器 id**：两个定时器共用 WM_TIMER，
		// 不区分的话图标自检会以 3 秒一次的频率跑，白白多调 API。
		switch wparam {
		case timerIconCheck:
			tray.checkIconAlive()
		default:
			go tray.refreshStateAsync()
		}
		return 0

	case wmUpdateReady:
		tray.onUpdateReady()
		return 0

	case wmCommand:
		// 菜单走 TPM_RETURNCMD 直接分发，这条留着给其它来源的命令消息
		tray.dispatchCommand(uint32(wparam) & 0xFFFF)
		return 0

	case wmClose:
		// 退出路径的收尾：把窗口销毁掉（随后会收到 WM_DESTROY → 退出消息循环）。
		// 必须在本线程做，所以 exit() 用 PostMessage(WM_CLOSE) 绕回这里。
		destroyWindow(hwnd)
		return 0

	case wmDestroy:
		postQuitMessage(0)
		return 0
	}

	// TaskbarCreated 不是固定常量，只能运行时比较注册出来的 id。
	// 放在 switch 后面：它的值可能和上面任何一个常量撞上，必须先判完。
	if tray.taskbarCreatedMsg != 0 && msg == tray.taskbarCreatedMsg {
		tray.reAddIcon("explorer 重启（TaskbarCreated）")
		return 0
	}

	return defWindowProc(hwnd, msg, wparam, lparam)
}

// showMenu 弹右键菜单。
//
// **必须在 UI 线程上同步调用 TrackPopupMenu。**
//
// 试过把它丢到独立线程/goroutine 弹，结果 TrackPopupMenu 立刻返回 0、
// 菜单根本不显示 —— 它要求调用线程就是拥有前台窗口的那条线程，
// 换到别的线程 Windows 直接拒绝（返回 0，不报错）。
//
// 同步阻塞是安全的：TrackPopupMenu 内部的消息泵会重入 wndProc，
// Go 的 syscall.NewCallback 支持这种重入（栈会增长）。真正需要避免的是
// **在别的线程弹**，以及**在 wndProc 里做别的耗时操作**。
//
// 前置条件：窗口必须是前台窗口，否则菜单显示后立刻被关掉。
// SetForegroundWindow 只在系统认可"用户刚有过输入"时才生效，
// 所以先发一个假的 ALT 键解锁——见 simulateAltKey 的注释。
func (t *TrayApp) showMenu() {
	defer func() {
		if r := recover(); r != nil {
			diagLog("showMenu: PANIC %v", r)
			buf := make([]byte, 8192)
			n := runtime.Stack(buf, false)
			diagLog("showMenu: stack\n%s", string(buf[:n]))
		}
	}()
	var pt point
	getCursorPos(&pt)

	// 前台切换：先发假 ALT 解锁权限，再 SetForegroundWindow。
	//
	// 有失败的可能（系统可能仍不认可前台切换），失败时菜单会立刻消失。
	// 所以做两轮重试：第二轮再来一次假按键 + 置前。
	// 实测偶发第一轮不生效，重试一次能覆盖绝大多数情况。
	simulateAltKey()
	if !setForegroundWindow(t.hwnd) {
		simulateAltKey()
		setForegroundWindow(t.hwnd)
	}

	menu := createPopupMenu()
	if menu == 0 {
		diagLog("showMenu: CreatePopupMenu 失败")
		return
	}
	defer destroyMenu(menu)

	appendMenu(menu, mfString, cmdOpenUI, "打开管理界面")
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, cmdStart, "启动")
	appendMenu(menu, mfString, cmdStop, "停止")
	appendMenu(menu, mfString, cmdRestart, "重启")
	appendMenu(menu, mfSeparator, 0, "")
	appendMenu(menu, mfString, cmdOpenURL, "打开访问地址")
	appendMenu(menu, mfString, cmdOpenDir, "打开安装目录")
	appendMenu(menu, mfString, cmdOpenLogs, "查看日志")
	appendMenu(menu, mfSeparator, 0, "")

	// 更新区：有新版才出现「更新到 vX.Y.Z」，检查更新常驻。
	// 升级进行中把两项都置灰，防止重复点。
	if t.upd.HasUpdate {
		label := "更新到 " + t.upd.Latest
		flags := uint32(mfString)
		if t.updating {
			flags |= mfGrayed
		}
		appendMenu(menu, flags, cmdApplyUpdate, label)
	}
	checkFlags := uint32(mfString)
	if t.updating {
		checkFlags |= mfGrayed
	}
	appendMenu(menu, checkFlags, cmdCheckUpdate, "检查更新")
	appendMenu(menu, mfSeparator, 0, "")

	appendMenu(menu, mfString, cmdExit, "退出")

	// TPM_RETURNCMD：菜单同步返回被点的项 id（且不再发 WM_COMMAND），
	// 直接在这里分发，省掉一次消息往返。
	cmd := trackPopupMenu(menu, tpmRightButton|tpmReturnCmd|tpmNonotify, pt.X, pt.Y, t.hwnd)

	// MSDN 的托盘示例在 TrackPopupMenu 之后都会补一条 WM_NULL，
	// 让菜单管理器知道"这一轮结束了"。漏掉的话菜单状态可能残留在
	// shell 里，下一次右键直接失效。
	postMessage(t.hwnd, wmNull, 0, 0)

	if cmd != 0 {
		t.dispatchCommand(uint32(cmd))
	}
}

// dispatchCommand 执行菜单命令。菜单点击和 WM_COMMAND 共用同一套分发。
func (t *TrayApp) dispatchCommand(cmd uint32) {
	switch cmd {
	case cmdOpenUI:
		go t.openUIAsync()
	case cmdStart:
		go t.startAsync()
	case cmdStop:
		go t.stopAsync()
	case cmdRestart:
		go t.restartAsync()
	case cmdOpenURL:
		u := t.url
		if u == "" {
			u = LastUrl(t.opt.Port)
		}
		Paths.OpenUrl(u)
	case cmdOpenDir:
		Paths.OpenFolder(Paths.Root())
	case cmdOpenLogs:
		Paths.OpenFolder(Paths.LogDir())
	case cmdCheckUpdate:
		go t.checkUpdateAsync()
	case cmdApplyUpdate:
		go t.applyUpdateAsync()
	case cmdExit:
		// 必须走 goroutine：exit() 里要 Stop()，最多等 20 秒，
		// 同步跑会把消息循环卡死（窗口无法重绘、连"未响应"都可能出现）。
		go t.exit()
	}
}

// ------------------------------------------------------------------ 看门狗

// uiHeartbeat 由 UI 线程定期递增，看门狗 goroutine 检查它是否停滞。
//
// **为什么需要它**：托盘主线程一旦卡死，用户看到的就是"右键没反应"，
// 但进程还活着、图标还在，从外部完全看不出卡在哪。有了心跳+日志，
// 就能在日志里直接看到"UI 线程从某一刻起不再响应"以及当时它在做什么。
var uiHeartbeat int64

// debugMsgs 为真时把每条窗口消息写进 diagLog，用于定位卡死点。
// 由环境变量 DSH_TRAY_DEBUG=1 开启，平时零开销。
var debugMsgs = os.Getenv("DSH_TRAY_DEBUG") == "1"

// startWatchdog 起一个后台 goroutine 监控 UI 线程心跳。
//
// 卡死超过 10 秒就把**所有 goroutine 的调用栈**写进日志。
// 这是唯一能在现场定位死锁的手段 —— 光靠猜"可能是哪个调用"效率太低，
// 实测靠这个直接看到了卡在哪一行。
func (t *TrayApp) startWatchdog() {
	go func() {
		last := atomic.LoadInt64(&uiHeartbeat)
		lastChange := time.Now()
		dumped := false
		for {
			time.Sleep(2 * time.Second)
			if t.exiting {
				return
			}
			cur := atomic.LoadInt64(&uiHeartbeat)
			if cur != last {
				last = cur
				lastChange = time.Now()
				dumped = false // 恢复了，下次卡死可以再 dump
				continue
			}
			stuck := time.Since(lastChange)
			if stuck >= 10*time.Second && !dumped {
				dumped = true
				diagLog("看门狗: UI 线程已停滞 %d 秒（heartbeat=%d）。dump 全部 goroutine 栈：",
					int(stuck.Seconds()), cur)
				diagLog("=== goroutine dump begin ===\n%s\n=== goroutine dump end ===",
					dumpAllGoroutines())
			}
		}
	}()
}

// dumpAllGoroutines 返回所有 goroutine 的调用栈文本。
func dumpAllGoroutines() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}

// ------------------------------------------------------------------ 状态

// nidMu 保护 t.nid 和所有 Shell_NotifyIcon 调用。
//
// **这是必须的**：t.nid 会被多处并发改动 ——
//   - UI 线程：showMenu 里读 Flags，checkIconAlive 里改 Flags 并调 NIM_MODIFY
//   - goroutine：setIcon / setText / balloon（refreshState、startAsync 等）
//
// 并发改同一个 NOTIFYICONDATAW 会让 Shell_NotifyIcon 收到撕裂的结构体
// （Size/Flags/Hwnd 对不上），shell 侧行为未定义 —— 实测表现就是
// 消息循环彻底卡死、连 ESC 和鼠标点击都无法唤醒。
var nidMu sync.Mutex

func (t *TrayApp) setText(text string) {
	if len(text) > 63 {
		text = truncateBytes(text, 63)
	}
	// TryLock：拿不到就跳过。图标文字晚 3 秒更新完全无所谓，
	// 但**在 UI 线程上等锁会直接冻结整个托盘** —— 那才是致命问题。
	if !nidMu.TryLock() {
		return
	}
	t.nid.Flags = nifMessage | nifIcon | nifTip
	copyUTF16(t.nid.Tip[:], text)
	shellNotifyIcon(nimModify, &t.nid)
	nidMu.Unlock()
}

// truncateBytes 按字符截断到 n 字节以内，避免把多字节汉字切一半。
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if b.Len()+len(string(r)) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (t *TrayApp) setIcon(h uintptr) {
	if h == 0 {
		return
	}
	if !nidMu.TryLock() {
		return
	}
	t.nid.Icon = h
	t.nid.Flags = nifMessage | nifIcon | nifTip
	shellNotifyIcon(nimModify, &t.nid)
	nidMu.Unlock()
}

func (t *TrayApp) balloon(title, text string, flags uint32) {
	if !nidMu.TryLock() {
		return
	}
	t.nid.Flags = nifInfo
	t.nid.InfoFlags = flags
	copyUTF16(t.nid.InfoTitle[:], title)
	copyUTF16(t.nid.Info[:], text)
	shellNotifyIcon(nimModify, &t.nid)
	t.nid.Flags = nifMessage | nifIcon | nifTip
	nidMu.Unlock()
}

// refreshStateAsync 轮询状态（每 3 秒）。
func (t *TrayApp) refreshStateAsync() {
	if t.exiting {
		return
	}
	t.refreshState()

	if t.wasRunning && !t.isRunning() && !t.busy && !t.exiting {
		t.balloon(trayTitle, "dsh web 已退出。右键图标可重新启动。", niifWarning)
	}
	t.wasRunning = t.isRunning()
}

func (t *TrayApp) isRunning() bool {
	return Paths.IsPortListening(t.opt.Port)
}

// refreshState 把当前状态刷到图标和提示文字上。
//
// **一次锁内同时改图标和文字**：原来分成 setIcon() + setText() 两次调用、
// 各拿一次锁，中间会被别的 goroutine 插进来。虽然加了锁不会写坏数据，
// 但会出现"图标换了一半、文字还是旧的"，而且两次锁增加了 UI 线程
// 与 goroutine 争抢的机会。
func (t *TrayApp) refreshState() {
	if t.exiting {
		return
	}
	running := t.isRunning()
	if t.busy {
		return
	}

	var icon uintptr
	var text string
	if running {
		icon = t.icons.running
		text = fmt.Sprintf("dsh web：运行中\n端口 %d", t.opt.Port)
	} else {
		icon = t.icons.stopped
		text = "dsh web：未运行"
	}
	if len(text) > 63 {
		text = truncateBytes(text, 63)
	}

	if !nidMu.TryLock() {
		return
	}
	if icon != 0 {
		t.nid.Icon = icon
	}
	t.nid.Flags = nifMessage | nifIcon | nifTip
	copyUTF16(t.nid.Tip[:], text)
	shellNotifyIcon(nimModify, &t.nid)
	nidMu.Unlock()
}

// ------------------------------------------------------------------ 动作

func (t *TrayApp) startAsync() {
	if t.busy {
		return
	}
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在启动...")

	url, err := Start(t.opt)
	if err != nil {
		t.url = ""
		t.balloon(trayTitle, "启动失败："+err.Error(), niifError)
	} else {
		t.url = url
	}
	t.busy = false
	invalidateSnapshot()
	t.refreshState()
}

func (t *TrayApp) stopAsync() {
	if t.busy {
		return
	}
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在停止...")

	n := Stop(t.opt.Port)
	t.url = ""
	t.busy = false
	t.wasRunning = false // 手动停止，别再当成意外退出提示一次
	invalidateSnapshot()
	t.refreshState()

	if n > 0 {
		t.balloon(trayTitle, "dsh web 已停止。", niifInfo)
	} else {
		t.balloon(trayTitle, "dsh web 未在运行。", niifInfo)
	}
}

func (t *TrayApp) restartAsync() {
	t.stopAsync()
	t.startAsync()
}

func (t *TrayApp) openUIAsync() {
	// 只认本端口的地址，历史遗留的别的端口要忽略
	u := t.url
	if u == "" {
		u = LastUrl(t.opt.Port)
	}
	if u != "" {
		Paths.OpenUrl(u)
		return
	}

	if !t.isRunning() {
		t.balloon(trayTitle, "dsh web 未运行，正在启动...", niifInfo)
		t.startAsync()
		// startAsync 是同步的（内部 Start() 最多等 120 秒）。
		// 它跑在 goroutine 里，不会卡 UI 线程，但这里必须重新读 t.url：
		// 启动完成后地址才可用。
		if fresh := t.url; fresh != "" {
			Paths.OpenUrl(fresh)
		} else if fresh := LastUrl(t.opt.Port); fresh != "" {
			Paths.OpenUrl(fresh)
		}
	} else {
		t.balloon(trayTitle, "服务在运行，但还没拿到带 token 的地址，请稍等几秒再点。", niifWarning)
	}
}

// checkUpdateAsync 手动检查一次更新（菜单点击/转交命令）。
func (t *TrayApp) checkUpdateAsync() {
	if t.updating {
		return
	}
	t.balloon(trayTitle, "正在检查更新...", niifInfo)

	st := checkUpdate()
	t.upd = st
	if st.Err != "" {
		updateLog("手动检查失败：%s", st.Err)
		t.balloon(trayTitle, "检查更新失败："+st.Err, niifWarning)
		return
	}
	updateLog("手动检查：当前 %s，最新 %s", st.Current, st.Latest)
	if st.HasUpdate {
		// 手动检查就是要看到结果，这里不走"只提示一次"的去重
		t.notifiedV = st.Latest
		t.balloon(trayTitle,
			fmt.Sprintf("发现新版本 %s（当前 %s），右键可更新。", st.Latest, st.Current),
			niifInfo)
	} else {
		t.balloon(trayTitle, "已是最新版本（"+st.Current+"）。", niifInfo)
	}
}

// applyUpdateAsync 执行升级。
//
// 阶段 A（下载）服务照常跑；阶段 B（换目录）会短暂停机。
// 整个流程在 goroutine 里，绝不能在 wndProc 里同步跑。
func (t *TrayApp) applyUpdateAsync() {
	if t.updating {
		return
	}
	if !t.upd.HasUpdate || t.upd.Latest == "" {
		t.balloon(trayTitle, "没有可用的新版本，请先检查更新。", niifWarning)
		return
	}

	target := t.upd.Latest
	t.updating = true
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在更新到 " + target + "...")
	t.balloon(trayTitle,
		"正在下载 "+target+"，服务暂时保持运行。完成后会自动重启。", niifInfo)

	logf := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		updateLog("%s", msg)
		diagLog("update: %s", msg)
	}

	upOpt := *t.opt
	upOpt.NoBrowser = true

	err := runUpdate(target, &upOpt, logf)

	t.updating = false
	t.busy = false
	invalidateSnapshot()

	if err != nil {
		t.setIcon(t.icons.stopped)
		t.refreshState()
		t.balloon(trayTitle, "更新失败："+err.Error(), niifError)
		return
	}

	// 升级成功后状态全变了：重新读版本、刷新地址
	t.url = LastUrl(t.opt.Port)
	t.upd = updateState{Current: target, Latest: target, CheckedAt: time.Now()}
	t.refreshState()
	t.balloon(trayTitle, "已更新到 "+target+"。", niifInfo)
}

// exit 退出托盘。
//
// **退出要顺手停掉它管的那个服务。** 用户点「退出」的意思是"托盘不要了，
// 它管的 dsh web 也别留着"；早先这里只收图标、不动进程，结果是退了托盘
// node 还在后台跑，下次启动还会撞端口占用。
//
// 停的仍然是 Stop(t.opt.Port) —— 只停命令行里 --port 等于本端口的那一个，
// 绝不波及别的端口上的实例（改这里之前先读 docs/PITFALLS.md 的「进程与端口」）。
//
// 注意这一段是**同步**跑的，Stop 里最多会等 20 秒等进程退出。
// dispatchCommand 用 goroutine 调本函数，所以不会卡住消息循环；
// 但不要在 UI 线程（wndProc）里直接调它。
func (t *TrayApp) exit() {
	if t.exiting {
		return
	}
	t.exiting = true

	// 收掉后台更新检查，别让它在退出过程中还发请求
	if t.updateStop != nil {
		close(t.updateStop)
		t.updateStop = nil
	}

	// 先把图标收掉，让用户立刻看到"退出了"，别等 Stop 慢慢等
	t.nid.Flags = nifMessage | nifIcon | nifTip
	shellNotifyIcon(nimDelete, &t.nid)

	n := Stop(t.opt.Port)
	diagLog("exit: port=%d stopped=%d", t.opt.Port, n)

	t.icons.close()

	// DestroyWindow 必须在创建窗口的那个线程（消息循环线程）上调用。
	// 本函数跑在 goroutine 里，所以不能直接 destroyWindow，
	// 改成往窗口投一个 WM_CLOSE，由 wndProc 在自己的线程上销毁。
	// （wndProc 的 wmClose 分支就是干这个的）
	postMessage(t.hwnd, wmClose, 0, 0)
}

// handleForwarded 处理第二个实例转交过来的命令。
func handleForwarded(argLine string) {
	args := strings.Fields(argLine)
	opt := parseOptions(args)

	if opt.StopOnly {
		tray.stopAsync()
		return
	}
	if opt.DryRun {
		tray.openUIAsync()
		return
	}
	tray.opt.Port = opt.Port
	tray.opt.NoBrowser = opt.NoBrowser
	tray.opt.KeepExisting = opt.KeepExisting
	tray.restartAsync()
}
