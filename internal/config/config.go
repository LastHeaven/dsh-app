//go:build windows

// Package config 定义命令行选项这一份共享数据结构。
//
// 单独成包是为了打破循环依赖：service / tray / updater 都要读 Port、
// KeepExisting 之类的字段，但它们不该反过来依赖命令行的解析逻辑。
package config

// DefaultPort 是 dsh web 不带 --port 时的默认端口。
const DefaultPort = 3080

// Options 是进程级配置，来自命令行，托盘和 Headless 两条路径共用。
type Options struct {
	Port         int
	NoBrowser    bool
	KeepExisting bool
	StopOnly     bool
	DryRun       bool
	Headless     bool
	ShowHelp     bool

	// 更新相关
	CheckUpdate   bool   // -CheckUpdate：查一次最新版就退出
	UpdateTo      string // -UpdateTo <ver>：升级到指定版本（空表示 latest）
	NoUpdateCheck bool   // -NoUpdateCheck：托盘不做后台检查
	Force         bool   // -Force：允许重装同版本（修复损坏的安装）
}

// Default 返回带默认端口的配置。
func Default() *Options {
	return &Options{Port: DefaultPort}
}
