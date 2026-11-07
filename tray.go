//go:build windows

package main

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// ------------------------------------------------------------------ 时间

func timeNow() time.Time { return time.Now() }

// ------------------------------------------------------------------ 托盘

const (
	trayTitle     = "dsh web"
	trayClassName = "DshWebTrayWnd"
	trayID        = 1
	timerPoll     = 1

	pollIntervalMs = 3000
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

	t.hwnd = createWindowEx(0, uintptr(unsafe.Pointer(className)), 0,
		wsPopup, 0, 0, 0, 0, hwndMessage, 0, instance)
	if t.hwnd == 0 {
		messageBox(0, "无法创建托盘窗口：%s", trayTitle, mbOK|mbIconInformation)
		return
	}

	t.addIcon()
	t.refreshState()

	// 轮询用 SetTimer，跟着消息循环走，不额外开线程。
	// **必须在这里设**：hwnd 是 Run() 内部创建的，放到 main 里会拿到 0。
	procSetTimer.Call(t.hwnd, timerPoll, pollIntervalMs, 0)

	// 首次启动：本端口的服务没在跑就拉起来
	if !Paths.IsPortListening(t.opt.Port) {
		go t.startAsync()
	}

	t.messageLoop()
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

func (t *TrayApp) messageLoop() {
	var msg msgStruct
	for {
		r := getMessage(&msg, 0, 0, 0)
		if r == 0 || r == -1 {
			break
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
		go tray.refreshStateAsync()
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
	return defWindowProc(hwnd, msg, wparam, lparam)
}

// showMenu 弹右键菜单。必须 SetForegroundWindow，否则点别处菜单不消失。
func (t *TrayApp) showMenu() {
	menu := createPopupMenu()
	if menu == 0 {
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
	appendMenu(menu, mfString, cmdExit, "退出")

	var pt point
	getCursorPos(&pt)
	setForegroundWindow(t.hwnd) // 不置前，点菜单外面菜单不会消失
	// TPM_RETURNCMD：菜单同步返回被点的项 id（且不再发 WM_COMMAND），
	// 直接在这里分发，省掉一次消息往返。
	cmd := trackPopupMenu(menu, tpmRightButton|tpmReturnCmd|tpmNonotify, pt.X, pt.Y, t.hwnd)
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
	case cmdExit:
		// 必须走 goroutine：exit() 里要 Stop()，最多等 20 秒，
		// 同步跑会把消息循环卡死（窗口无法重绘、连"未响应"都可能出现）。
		go t.exit()
	}
}

// ------------------------------------------------------------------ 状态

func (t *TrayApp) setText(text string) {
	if len(text) > 63 {
		text = truncateBytes(text, 63)
	}
	t.nid.Flags = nifMessage | nifIcon | nifTip
	copyUTF16(t.nid.Tip[:], text)
	shellNotifyIcon(nimModify, &t.nid)
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
	t.nid.Icon = h
	t.nid.Flags = nifMessage | nifIcon | nifTip
	shellNotifyIcon(nimModify, &t.nid)
}

func (t *TrayApp) balloon(title, text string, flags uint32) {
	t.nid.Flags = nifInfo
	t.nid.InfoFlags = flags
	copyUTF16(t.nid.InfoTitle[:], title)
	copyUTF16(t.nid.Info[:], text)
	shellNotifyIcon(nimModify, &t.nid)
	t.nid.Flags = nifMessage | nifIcon | nifTip
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

func (t *TrayApp) refreshState() {
	if t.exiting {
		return
	}
	running := t.isRunning()
	if t.busy {
		return
	}
	if running {
		t.setIcon(t.icons.running)
		t.setText(fmt.Sprintf("dsh web：运行中\n端口 %d", t.opt.Port))
	} else {
		t.setIcon(t.icons.stopped)
		t.setText("dsh web：未运行")
	}
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
		if fresh := t.url; fresh != "" {
			Paths.OpenUrl(fresh)
		} else if fresh := LastUrl(t.opt.Port); fresh != "" {
			Paths.OpenUrl(fresh)
		}
	} else {
		t.balloon(trayTitle, "服务在运行，但还没拿到带 token 的地址，请稍等几秒再点。", niifWarning)
	}
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
