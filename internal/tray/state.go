//go:build windows

package tray

import (
	"fmt"
	"strings"
	"sync"

	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

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

func (t *App) setText(text string) {
	if len(text) > 63 {
		text = truncateBytes(text, 63)
	}
	// TryLock：拿不到就跳过。图标文字晚 3 秒更新完全无所谓，
	// 但**在 UI 线程上等锁会直接冻结整个托盘** —— 那才是致命问题。
	if !nidMu.TryLock() {
		return
	}
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	copyUTF16(t.nid.Tip[:], text)
	win32.ShellNotifyIcon(win32.NIMModify, &t.nid)
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

func (t *App) setIcon(h uintptr) {
	if h == 0 {
		return
	}
	if !nidMu.TryLock() {
		return
	}
	t.nid.Icon = h
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	win32.ShellNotifyIcon(win32.NIMModify, &t.nid)
	nidMu.Unlock()
}

func (t *App) balloon(titleText, text string, flags uint32) {
	if !nidMu.TryLock() {
		return
	}
	t.nid.Flags = win32.NIFInfo
	t.nid.InfoFlags = flags
	copyUTF16(t.nid.InfoTitle[:], titleText)
	copyUTF16(t.nid.Info[:], text)
	win32.ShellNotifyIcon(win32.NIMModify, &t.nid)
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	nidMu.Unlock()
}

// refreshStateAsync 轮询状态（每 3 秒）。
func (t *App) refreshStateAsync() {
	if t.isExiting() {
		return
	}
	t.refreshState()

	// 读 wasRunning、算 isRunning、再写回：跨了多次状态访问，
	// 中间可能被别的 goroutine 改，但这里只是"要不要弹一次气泡"，
	// 用一次快照判断即可，不需要原子性。
	if t.getWasRunning() && !t.isRunning() && !t.isBusy() && !t.isExiting() {
		t.balloon(title, "dsh web 已退出。右键图标可重新启动。", win32.NIIFWarning)
	}
	t.setWasRunning(t.isRunning())
}

func (t *App) isRunning() bool {
	return paths.IsPortListening(t.opt.Port)
}

// refreshState 把当前状态刷到图标和提示文字上。
//
// **一次锁内同时改图标和文字**：原来分成 setIcon() + setText() 两次调用、
// 各拿一次锁，中间会被别的 goroutine 插进来。虽然加了锁不会写坏数据，
// 但会出现"图标换了一半、文字还是旧的"，而且两次锁增加了 UI 线程
// 与 goroutine 争抢的机会。
func (t *App) refreshState() {
	if t.isExiting() {
		return
	}
	running := t.isRunning()
	if t.isBusy() {
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
	t.nid.Flags = win32.NIFMessage | win32.NIFIcon | win32.NIFTip
	copyUTF16(t.nid.Tip[:], text)
	win32.ShellNotifyIcon(win32.NIMModify, &t.nid)
	nidMu.Unlock()
}
