//go:build windows

// Package applog 是运行期日志。
//
// 托盘程序没有控制台，出了问题只能靠文件排查，所以三条日志各管一摊：
//
//	Diag        tray-diag.log    托盘自身的生命周期与看门狗（排障主入口）
//	UpdateLog   logs\update.log  更新检查与升级流水
//	Headless    headless.log     命令行模式（-Stop/-DryRun）的输出留档
//
// 全部是 UTF-8 字节流（os.File.WriteString 不做编码转换）。用 PowerShell 的
// Get-Content 直接看会像乱码，那是按 GBK 解码导致的 ——
// 请用 Get-Content -Encoding UTF8，不要以为文件坏了。
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LastHeaven/dsh-app/internal/paths"
)

// Timestamp 返回时分秒，用于日志行前缀。
func Timestamp() string {
	t := time.Now()
	return fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
}

// Now 是给时间戳用的当前时刻（单独函数便于测试替换）。
func Now() time.Time { return time.Now() }

func appendLine(path, line string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// Diag 写诊断日志到安装目录下的 tray-diag.log。
func Diag(format string, a ...interface{}) {
	appendLine(paths.Join(paths.Root(), "tray-diag.log"),
		Timestamp()+"  "+fmt.Sprintf(format, a...)+"\r\n")
}

// UpdateLog 写更新日志到 logs\update.log。
func UpdateLog(format string, a ...interface{}) {
	t := Now()
	line := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d  ",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()) +
		fmt.Sprintf(format, a...) + "\r\n"
	appendLine(filepath.Join(paths.LogDir(), "update.log"), line)
}

// Headless 写命令行模式的输出留档。
func Headless(text string) {
	appendLine(paths.Join(paths.Root(), "headless.log"),
		Timestamp()+"  "+text+"\r\n")
}
