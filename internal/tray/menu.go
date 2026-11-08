//go:build windows

package tray

import (
	"runtime"

	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/service"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

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
// 所以先发一个假的 ALT 键解锁——见 win32.SimulateAltKey 的注释。
func (t *App) showMenu() {
	defer func() {
		if r := recover(); r != nil {
			Diag("showMenu: PANIC %v", r)
			buf := make([]byte, 8192)
			n := runtime.Stack(buf, false)
			Diag("showMenu: stack\n%s", string(buf[:n]))
		}
	}()
	var pt point
	win32.GetCursorPos(&pt)

	// 前台切换：先发假 ALT 解锁权限，再 SetForegroundWindow。
	//
	// 有失败的可能（系统可能仍不认可前台切换），失败时菜单会立刻消失。
	// 所以做两轮重试：第二轮再来一次假按键 + 置前。
	// 实测偶发第一轮不生效，重试一次能覆盖绝大多数情况。
	win32.SimulateAltKey()
	if !win32.SetForegroundWindow(t.hwnd) {
		win32.SimulateAltKey()
		win32.SetForegroundWindow(t.hwnd)
	}

	menu := win32.CreatePopupMenu()
	if menu == 0 {
		Diag("showMenu: CreatePopupMenu 失败")
		return
	}
	defer win32.DestroyMenu(menu)

	win32.AppendMenu(menu, win32.MFString, cmdOpenUI, "打开管理界面")
	win32.AppendMenu(menu, win32.MFSeparator, 0, "")
	win32.AppendMenu(menu, win32.MFString, cmdStart, "启动")
	win32.AppendMenu(menu, win32.MFString, cmdStop, "停止")
	win32.AppendMenu(menu, win32.MFString, cmdRestart, "重启")
	win32.AppendMenu(menu, win32.MFSeparator, 0, "")
	win32.AppendMenu(menu, win32.MFString, cmdOpenURL, "打开访问地址")
	win32.AppendMenu(menu, win32.MFString, cmdOpenDir, "打开安装目录")
	win32.AppendMenu(menu, win32.MFString, cmdOpenLogs, "查看日志")
	win32.AppendMenu(menu, win32.MFSeparator, 0, "")

	// 更新区：有新版才出现「更新到 vX.Y.Z」，检查更新常驻。
	// 升级进行中把两项都置灰，防止重复点。
	if t.upd.HasUpdate {
		label := "更新到 " + t.upd.Latest
		flags := uint32(win32.MFString)
		if t.updating {
			flags |= win32.MFGrayed
		}
		win32.AppendMenu(menu, flags, cmdApplyUpdate, label)
	}
	checkFlags := uint32(win32.MFString)
	if t.updating {
		checkFlags |= win32.MFGrayed
	}
	win32.AppendMenu(menu, checkFlags, cmdCheckUpdate, "检查更新")
	win32.AppendMenu(menu, win32.MFSeparator, 0, "")

	win32.AppendMenu(menu, win32.MFString, cmdExit, "退出")

	// TPM_RETURNCMD：菜单同步返回被点的项 id（且不再发 WM_COMMAND），
	// 直接在这里分发，省掉一次消息往返。
	cmd := win32.TrackPopupMenu(menu,
		win32.TPMRightButton|win32.TPMReturnCmd|win32.TPMNonotify, pt.X, pt.Y, t.hwnd)

	// MSDN 的托盘示例在 TrackPopupMenu 之后都会补一条 WM_NULL，
	// 让菜单管理器知道"这一轮结束了"。漏掉的话菜单状态可能残留在
	// shell 里，下一次右键直接失效。
	win32.PostMessage(t.hwnd, win32.WMNull, 0, 0)

	if cmd != 0 {
		t.dispatchCommand(uint32(cmd))
	}
}

// dispatchCommand 执行菜单命令。菜单点击和 WM_COMMAND 共用同一套分发。
func (t *App) dispatchCommand(cmd uint32) {
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
			u = service.LastURL(t.opt.Port)
		}
		paths.OpenURL(u)
	case cmdOpenDir:
		paths.OpenFolder(paths.Root())
	case cmdOpenLogs:
		paths.OpenFolder(paths.LogDir())
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
