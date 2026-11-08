//go:build windows

package tray

import (
	"strings"
	"time"

	"github.com/LastHeaven/dsh-app/internal/cli"
	"github.com/LastHeaven/dsh-app/internal/updater"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

// updateLoop 后台周期检查更新。退出时通过 updateStop 收掉。
func (t *App) updateLoop() {
	select {
	case <-time.After(updater.InitialDelay):
	case <-t.updateStop:
		return
	}

	for {
		if t.exiting {
			return
		}
		st := updater.Check()
		if st.Err != "" {
			Diag("checkUpdate: %s", st.Err)
			updater.Log("检查更新失败：%s", st.Err)
		} else {
			updater.Log("检查更新：当前 %s，最新 %s，有更新=%v",
				st.Current, st.Latest, st.HasUpdate)
		}
		t.upd = st
		// 回窗口线程：菜单和气泡都必须由创建窗口的线程改
		win32.PostMessage(t.hwnd, win32.WMUpdateReady, 0, 0)

		select {
		case <-time.After(updater.Interval):
		case <-t.updateStop:
			return
		}
	}
}

// onUpdateReady 在 UI 线程处理检查结果（由 WMUpdateReady 触发）。
func (t *App) onUpdateReady() {
	if t.exiting || !t.upd.HasUpdate {
		return
	}
	// 同一个版本只提示一次，别每 6 小时骚扰
	if t.notifiedV == t.upd.Latest {
		return
	}
	t.notifiedV = t.upd.Latest
	t.balloon(title, "dsh 有新版本 "+t.upd.Latest+"，右键图标可更新。", win32.NIIFInfo)
}

// HandleForwarded 处理第二个实例转交过来的命令。
func HandleForwarded(argLine string) {
	if current == nil {
		return
	}
	args := strings.Fields(argLine)
	opt := cli.ParseOptions(args)

	if opt.StopOnly {
		current.stopAsync()
		return
	}
	if opt.DryRun {
		current.openUIAsync()
		return
	}
	current.opt.Port = opt.Port
	current.opt.NoBrowser = opt.NoBrowser
	current.opt.KeepExisting = opt.KeepExisting
	current.restartAsync()
}
