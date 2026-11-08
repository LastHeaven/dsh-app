//go:build windows

package tray

import (
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

// Run 建隐藏消息窗口、装托盘图标、跑消息循环。
//
// **必须在 main 里 runtime.LockOSThread 之后调用**：窗口、定时器、
// 消息队列全部绑定在创建它们的 OS 线程上，goroutine 一旦迁移线程，
// GetMessage 就等不到本线程的消息，表现成"托盘点了没反应"。
func (t *App) Run() {
	current = t

	// 只收消息、永不显示的窗口
	instance := win32.GetModuleHandle()
	classNameP := win32.UTF16Ptr(className)

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   syscall.NewCallback(wndProc),
		Instance:  instance,
		ClassName: classNameP,
	}
	if win32.RegisterClassEx(&wc) == 0 {
		// 类已注册也继续（重复运行不会到这儿，但别硬失败）
		_ = 0
	}
	runtime.KeepAlive(classNameP)

	// 窗口用**不可见的普通顶层窗口**，不是 HWND_MESSAGE。
	//
	// 为什么不用 HWND_MESSAGE：它是"仅消息窗口"，收不到广播消息，
	// 而 explorer 重启时发的 TaskbarCreated 正是广播（HWND_BROADCAST）。
	// 实测确认：用 HWND_MESSAGE 时整条 explorer 重启恢复链路是死的，
	// 只能靠 30 秒一次的自检兜底，中间那段时间右键是坏的。
	//
	// 普通顶层窗口 + WS_POPUP + 不 ShowWindow，用户看不见，但能收到广播。
	t.hwnd = win32.CreateWindowEx(0, uintptr(unsafe.Pointer(classNameP)), 0,
		win32.WSPopup, 0, 0, 0, 0, 0, 0, instance)
	if t.hwnd == 0 {
		win32.MessageBox(0, "无法创建托盘窗口。", title, win32.MBOK|win32.MBIconInformation)
		return
	}

	t.addIcon()
	t.refreshState()

	// explorer 重启（崩溃恢复、开机后重登录）会广播 TaskbarCreated。
	// 不处理的话图标从此消失、点击也送不到窗口 —— 这是"用久了右键菜单
	// 打不开"最主要的成因之一。
	t.taskbarCreatedMsg = win32.RegisterWindowMessage("TaskbarCreated")
	Diag("TaskbarCreated msg id = 0x%X", t.taskbarCreatedMsg)

	// 轮询用 SetTimer，跟着消息循环走，不额外开线程。
	// **必须在这里设**：hwnd 是 Run() 内部创建的，放到 main 里会拿到 0。
	win32.SetTimer(t.hwnd, timerPoll, pollIntervalMs)

	// 图标健康检查用另一个定时器，频率低一些。
	// 光靠 TaskbarCreated 不够：某些情况下 explorer 不广播、或者图标
	// 被通知区"隐藏"后丢失，只能靠主动探测发现。
	win32.SetTimer(t.hwnd, timerIconCheck, iconCheckIntervalMs)

	// 首次启动：本端口的服务没在跑就拉起来
	if !paths.IsPortListening(t.opt.Port) {
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

func (t *App) addIcon() {
	t.nid = notifyIconData{
		Wnd:             t.hwnd,
		ID:              trayID,
		Flags:           win32.NIFMessage | win32.NIFIcon | win32.NIFTip,
		CallbackMessage: win32.WMApp,
		Icon:            t.icons.stopped,
	}
	t.nid.Size = uint32(unsafe.Sizeof(t.nid))
	copyUTF16(t.nid.Tip[:], title)
	ok := win32.ShellNotifyIcon(win32.NIMAdd, &t.nid)
	Diag("addIcon: ok=%v hwnd=%d icon=%d running=%d stopped=%d busy=%d",
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
func (t *App) reAddIcon(reason string) bool {
	if t.exiting || t.hwnd == 0 {
		return false
	}

	// 本函数既可能被 UI 线程调（TaskbarCreated、checkIconAlive），
	// 也可能被 goroutine 调。**UI 线程上不能阻塞等锁** ——
	// 这里统一用 TryLock，拿不到就交给下一轮：
	// TaskbarCreated 有 10 秒一次的自检兜底，晚一点重建没关系，
	// 但让 UI 线程卡住等锁会直接导致"整个托盘点不动"。
	if !nidMu.TryLock() {
		Diag("reAddIcon: reason=%s 拿不到锁，跳过本轮", reason)
		return false
	}

	win32.ShellNotifyIcon(win32.NIMDelete, &t.nid)

	// 重建时把 nid 恢复到"完整状态"：图标 + 提示文字 + 回调消息
	// 缺任何一个，图标会显示但不响应点击。
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	t.nid.CallbackMessage = win32.WMApp
	if t.nid.Icon == 0 {
		t.nid.Icon = t.icons.stopped
	}

	ok := win32.ShellNotifyIcon(win32.NIMAdd, &t.nid)
	nidMu.Unlock()

	Diag("reAddIcon: reason=%s ok=%v hwnd=%d", reason, ok, t.hwnd)
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
func (t *App) checkIconAlive() {
	if t.exiting || t.hwnd == 0 {
		return
	}
	if !nidMu.TryLock() {
		// 拿不到锁说明有 goroutine 正在改状态，跳过这轮
		return
	}
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	ok := win32.ShellNotifyIcon(win32.NIMModify, &t.nid)
	nidMu.Unlock()
	if ok {
		return // 图标还在，一切正常
	}
	Diag("checkIconAlive: NIM_MODIFY 失败，图标已失效，尝试重建")
	if !t.reAddIcon("自检发现图标失效") {
		Diag("checkIconAlive: 重建后备仍报错，等下一轮（shell 可能还没就绪）")
	}
}

func (t *App) messageLoop() {
	var msg msgStruct
	for {
		r := win32.GetMessage(&msg, 0, 0, 0)
		if r == 0 {
			// 正常的 WM_QUIT：窗口已销毁，可以退出
			Diag("messageLoop: WM_QUIT")
			return
		}
		if r == -1 {
			// **这是个真实踩到的坑**：GetMessage 返回 -1 表示出错（不是退出）。
			// 直接 break 出循环会让 Run() 返回、进程却还活着 ——
			// 变成一个"没有窗口的托盘空壳"：图标看着还在，
			// 但点击事件送不到窗口，右键菜单永远打不开。
			Diag("messageLoop: GetMessage 返回 -1（错误），退出进程以免留下无窗口的托盘空壳")
			return
		}
		win32.TranslateMessage(&msg)
		win32.DispatchMessage(&msg)
	}
}

// wndProc 是回调，**不能**做耗时操作（会卡住整个托盘）。
func wndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	if current == nil {
		return win32.DefWindowProc(hwnd, msg, wparam, lparam)
	}

	// 每次进回调先打心跳：看门狗据此判断 UI 线程是否还活着。
	beat()

	// 调试模式：把每条消息都记进日志，用于定位卡死点。
	// 用环境变量开启，平时零开销。
	if debugMsgs {
		Diag("wndProc: msg=0x%04X wparam=0x%X lparam=0x%X", msg, wparam, lparam)
	}

	switch msg {
	case win32.WMApp:
		switch uint32(lparam) & 0xFFFF {
		case win32.WMLButtonUp:
			go current.openUIAsync()
		case win32.WMRButtonUp:
			current.showMenu()
		}
		return 0

	case win32.WMTimer:
		// **必须按 wparam 区分定时器 id**：两个定时器共用 WM_TIMER，
		// 不区分的话图标自检会以 3 秒一次的频率跑，白白多调 API。
		switch wparam {
		case timerIconCheck:
			current.checkIconAlive()
		default:
			go current.refreshStateAsync()
		}
		return 0

	case win32.WMUpdateReady:
		current.onUpdateReady()
		return 0

	case win32.WMCommand:
		// 菜单走 TPM_RETURNCMD 直接分发，这条留着给其它来源的命令消息
		current.dispatchCommand(uint32(wparam) & 0xFFFF)
		return 0

	case win32.WMClose:
		// 退出路径的收尾：把窗口销毁掉（随后会收到 WM_DESTROY → 退出消息循环）。
		// 必须在本线程做，所以 exit() 用 PostMessage(WM_CLOSE) 绕回这里。
		win32.DestroyWindow(hwnd)
		return 0

	case win32.WMDestroy:
		win32.PostQuitMessage(0)
		return 0
	}

	// TaskbarCreated 不是固定常量，只能运行时比较注册出来的 id。
	// 放在 switch 后面：它的值可能和上面任何一个常量撞上，必须先判完。
	if current.taskbarCreatedMsg != 0 && msg == current.taskbarCreatedMsg {
		current.reAddIcon("explorer 重启（TaskbarCreated）")
		return 0
	}

	return win32.DefWindowProc(hwnd, msg, wparam, lparam)
}
