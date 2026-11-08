//go:build windows

package tray

import (
	"github.com/LastHeaven/dsh-app/internal/updater"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

// 这些别名的唯一目的是让托盘代码读起来不必到处带包名前缀。
// 它们是**类型别名**（带 =），不是新类型，赋值天然兼容。
type (
	notifyIconData = win32.NotifyIconData
	updateState    = updater.State
	msgStruct      = win32.Msg
	point          = win32.Point
	wndClassEx     = win32.WndClassEx
)
