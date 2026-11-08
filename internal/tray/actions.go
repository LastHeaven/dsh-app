//go:build windows

package tray

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/service"
	"github.com/LastHeaven/dsh-app/internal/updater"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

// ---------------------------------------------------------------- 看门狗

// startWatchdog 起一个后台 goroutine 监控 UI 线程心跳。
//
// 卡死超过 10 秒就把**所有 goroutine 的调用栈**写进日志。
// 这是唯一能在现场定位死锁的手段 —— 光靠猜"可能是哪个调用"效率太低，
// 实测靠这个直接看到了卡在哪一行。
func (t *App) startWatchdog() {
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
				Diag("看门狗: UI 线程已停滞 %d 秒（heartbeat=%d）。dump 全部 goroutine 栈：",
					int(stuck.Seconds()), cur)
				Diag("=== goroutine dump begin ===\n%s\n=== goroutine dump end ===",
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

// ---------------------------------------------------------------- 动作

func (t *App) startAsync() {
	if t.busy {
		return
	}
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在启动...")

	url, err := service.Start(t.opt)
	if err != nil {
		t.url = ""
		t.balloon(title, "启动失败："+err.Error(), win32.NIIFError)
	} else {
		t.url = url
	}
	t.busy = false
	t.refreshState()
}

func (t *App) stopAsync() {
	if t.busy {
		return
	}
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在停止...")

	n := service.Stop(t.opt.Port)
	t.url = ""
	t.busy = false
	t.wasRunning = false // 手动停止，别再当成意外退出提示一次
	t.refreshState()

	if n > 0 {
		t.balloon(title, "dsh web 已停止。", win32.NIIFInfo)
	} else {
		t.balloon(title, "dsh web 未在运行。", win32.NIIFInfo)
	}
}

func (t *App) restartAsync() {
	t.stopAsync()
	t.startAsync()
}

func (t *App) openUIAsync() {
	// 只认本端口的地址，历史遗留的别的端口要忽略
	u := t.url
	if u == "" {
		u = service.LastURL(t.opt.Port)
	}
	if u != "" {
		paths.OpenURL(u)
		return
	}

	if !t.isRunning() {
		t.balloon(title, "dsh web 未运行，正在启动...", win32.NIIFInfo)
		t.startAsync()
		// startAsync 是同步的（内部 Start() 最多等 120 秒）。
		// 它跑在 goroutine 里，不会卡 UI 线程，但这里必须重新读 t.url：
		// 启动完成后地址才可用。
		if fresh := t.url; fresh != "" {
			paths.OpenURL(fresh)
		} else if fresh := service.LastURL(t.opt.Port); fresh != "" {
			paths.OpenURL(fresh)
		}
	} else {
		t.balloon(title, "服务在运行，但还没拿到带 token 的地址，请稍等几秒再点。", win32.NIIFWarning)
	}
}

// checkUpdateAsync 手动检查一次更新（菜单点击/转交命令）。
func (t *App) checkUpdateAsync() {
	if t.updating {
		return
	}
	t.balloon(title, "正在检查更新...", win32.NIIFInfo)

	st := updater.Check()
	t.upd = st
	if st.Err != "" {
		updater.Log("手动检查失败：%s", st.Err)
		t.balloon(title, "检查更新失败："+st.Err, win32.NIIFWarning)
		return
	}
	updater.Log("手动检查：当前 %s，最新 %s", st.Current, st.Latest)
	if st.HasUpdate {
		// 手动检查就是要看到结果，这里不走"只提示一次"的去重
		t.notifiedV = st.Latest
		t.balloon(title,
			"发现新版本 "+st.Latest+"（当前 "+st.Current+"），右键可更新。",
			win32.NIIFInfo)
	} else {
		t.balloon(title, "已是最新版本（"+st.Current+"）。", win32.NIIFInfo)
	}
}

// applyUpdateAsync 执行升级。
//
// 阶段 A（下载）服务照常跑；阶段 B（换目录）会短暂停机。
// 整个流程在 goroutine 里，绝不能在 wndProc 里同步跑。
func (t *App) applyUpdateAsync() {
	if t.updating {
		return
	}
	if !t.upd.HasUpdate || t.upd.Latest == "" {
		t.balloon(title, "没有可用的新版本，请先检查更新。", win32.NIIFWarning)
		return
	}

	target := t.upd.Latest
	t.updating = true
	t.busy = true
	t.setIcon(t.icons.busy)
	t.setText("dsh web：正在更新到 " + target + "...")
	t.balloon(title,
		"正在下载 "+target+"，服务暂时保持运行。完成后会自动重启。", win32.NIIFInfo)

	logf := func(format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		updater.Log("%s", msg)
		Diag("update: %s", msg)
	}

	upOpt := *t.opt
	upOpt.NoBrowser = true

	err := updater.Run(target, &upOpt, logf)

	t.updating = false
	t.busy = false

	if err != nil {
		t.setIcon(t.icons.stopped)
		t.refreshState()
		t.balloon(title, "更新失败："+err.Error(), win32.NIIFError)
		return
	}

	// 升级成功后状态全变了：重新读版本、刷新地址
	t.url = service.LastURL(t.opt.Port)
	t.upd = updateState{Current: target, Latest: target, CheckedAt: time.Now()}
	t.refreshState()
	t.balloon(title, "已更新到 "+target+"。", win32.NIIFInfo)
}

// exit 退出托盘。
//
// **退出要顺手停掉它管的那个服务。** 用户点「退出」的意思是"托盘不要了，
// 它管的 dsh web 也别留着"；早先这里只收图标、不动进程，结果是退了托盘
// node 还在后台跑，下次启动还会撞端口占用。
//
// 停的仍然是 service.Stop(t.opt.Port) —— 只停命令行里 --port 等于本端口
// 的那一个，绝不波及别的端口上的实例（改这里之前先读 docs/PITFALLS.md）。
//
// 注意这一段是**同步**跑的，Stop 里最多会等 20 秒等进程退出。
// dispatchCommand 用 goroutine 调本函数，所以不会卡住消息循环；
// 但不要在 UI 线程（wndProc）里直接调它。
func (t *App) exit() {
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
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	win32.ShellNotifyIcon(win32.NIMDelete, &t.nid)

	n := service.Stop(t.opt.Port)
	Diag("exit: port=%d stopped=%d", t.opt.Port, n)

	t.icons.close()

	// DestroyWindow 必须在创建窗口的那个线程（消息循环线程）上调用。
	// 本函数跑在 goroutine 里，所以不能直接 DestroyWindow，
	// 改成往窗口投一个 WM_CLOSE，由 wndProc 在自己的线程上销毁。
	win32.PostMessage(t.hwnd, win32.WMClose, 0, 0)
}
