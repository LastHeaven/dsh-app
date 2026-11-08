//go:build windows

// Package paths 管运行期路径、文件小工具，以及「用哪个程序打开什么」。
//
// 和进程/服务无关的纯路径逻辑放这里，避免 service 和 tray 互相 import。
package paths

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/LastHeaven/dsh-app/internal/win32"
)

// Root 定位安装目录。
//
// 现在的规则：**以 exe 自己所在目录为准**，不再靠"旁边有没有某个文件"来认。
//
// 历史原因：早先靠 dsh-web-restart.ps1 / last-url.txt 是否存在来判定，
// 但这两个文件后来都被删了（脚本改成备用入口又整体移除，地址文件是运行产物），
// 判定就永远失效、只能吃 D:\agents\dsh 这个写死的兜底 —— 等于把目录
// 绑死在固定路径上，挪个位置日志就写错地方。
//
// 唯一保留的兜底：exe 所在目录不可写时（比如放在 Program Files）才退到
// D:\agents\dsh，避免日志静默写失败。
func Root() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		if wd, e := os.Getwd(); e == nil {
			return wd
		}
		return "."
	}
	dir := filepath.Dir(exe)
	if dirWritable(dir) {
		return dir
	}

	const fallback = `D:\agents\dsh`
	if st, e := os.Stat(fallback); e == nil && st.IsDir() && dirWritable(fallback) {
		return fallback
	}
	return dir
}

// dirWritable 探测目录是否可写。
// 不真的建日志文件，只试建一个临时名再删掉，避免副作用。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".dsh-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

func LogDir() string  { return filepath.Join(Root(), "logs") }
func OutLog() string  { return filepath.Join(LogDir(), "dsh-web.out.log") }
func ErrLog() string  { return filepath.Join(LogDir(), "dsh-web.err.log") }
func UrlFile() string { return filepath.Join(Root(), "last-url.txt") }

// Join 拼路径，兼容调用方传进来的结尾分隔符。
func Join(dir, name string) string {
	if dir == "" {
		return name
	}
	if strings.HasSuffix(dir, `\`) || strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + `\` + name
}

func FileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func DirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// SafeRead 读文件，出错一律返回空串（调用方都在做"读不到就降级"）。
func SafeRead(p string) string {
	if !FileExists(p) {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func TryDelete(p string) {
	if FileExists(p) {
		_ = os.Remove(p)
	}
}

// TrimForMsg 截断长文本用于错误提示。
func TrimForMsg(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// IsPortListening 端口上是否有 LISTEN。
func IsPortListening(port int) bool {
	return win32.PortOwnerPid(port) > 0
}

// URLPort 取地址里的端口，用于校验 last-url.txt。
func URLPort(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return 0, false
	}
	p := u.Port()
	if p == "" {
		if u.Scheme == "https" {
			return 443, true
		}
		return 80, true
	}
	var n int
	if _, err := fmt.Sscanf(p, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// ---------------------------------------------------------------- 打开

// OpenURL 用系统默认浏览器打开地址。
//
// **不要用 exec.Command("explorer.exe", url)** —— 实测这条会让 explorer
// 打开「文档」文件夹而不是浏览器。原因：Go 的 exec.Command 会把参数按
// MS 转义规则拼成命令行，explorer.exe 对引号的处理很特别，带引号的 URL
// 被它当成"找不到的路径"，于是退化成打开默认文件夹。
//
// 正确做法是 ShellExecuteW：它就是 C# 版 UseShellExecute=true 背后的东西，
// 由 shell 按 URL 协议去找默认浏览器，且浏览器保持普通权限。
func OpenURL(u string) {
	u = strings.TrimSpace(u)
	if u == "" {
		return
	}
	// 只放行 http/https，防止把奇怪字符串交给 shell
	low := strings.ToLower(u)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return
	}
	if win32.ShellExecuteOpen(u) {
		return
	}
	// 兜底：走 FileProtocolHandler
	_ = startDetached("rundll32.exe", []string{"url.dll,FileProtocolHandler", u})
}

// OpenFolder 打开资源管理器窗口。这里是**真的要打开文件夹**，
// 用 explorer.exe 是对的（而且必须带引号处理，见 startDetached 说明）。
func OpenFolder(p string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return
	}
	if !DirExists(p) {
		_ = os.MkdirAll(p, 0o755)
	}
	_ = startDetached("explorer.exe", []string{p})
}

// startDetached 用 CreateProcess 起一个独立进程。
//
// **注意**：只用它启动「真的要开文件/文件夹」的程序（explorer、rundll32）。
// 不要用它打开网页 —— 见 OpenURL 的说明。
func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait，让它自生自灭；但必须释放句柄，否则僵尸句柄累积
	_ = cmd.Process.Release()
	return nil
}

// ---------------------------------------------------------------- 定位 dsh

// ResolveNode 找 node.exe：先扫 PATH，再试常见安装位置。
func ResolveNode() string {
	for _, raw := range strings.Split(os.Getenv("PATH"), ";") {
		dir := strings.Trim(strings.TrimSpace(raw), `"`)
		if dir == "" {
			continue
		}
		c := filepath.Join(dir, "node.exe")
		if FileExists(c) {
			return c
		}
	}
	cands := []string{
		filepath.Join(envOr("ProgramFiles", `C:\Program Files`), "nodejs", "node.exe"),
		filepath.Join(envOr("ProgramFiles(x86)", `C:\Program Files (x86)`), "nodejs", "node.exe"),
		filepath.Join(envOr("LOCALAPPDATA", ""), "Programs", "nodejs", "node.exe"),
	}
	for _, c := range cands {
		if FileExists(c) {
			return c
		}
	}
	return ""
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// ResolveDshBin 找 @deepseek-ai/dsh/lib/bin.js。
//
// 顺序：PATH 上的 dsh shim → npm root -g → 固定候选目录。
func ResolveDshBin() string {
	const rel = `node_modules\@deepseek-ai\dsh\lib\bin.js`

	for _, name := range []string{"dsh.cmd", "dsh.ps1", "dsh.bat", "dsh.exe", "dsh"} {
		for _, raw := range strings.Split(os.Getenv("PATH"), ";") {
			dir := strings.Trim(strings.TrimSpace(raw), `"`)
			if dir == "" {
				continue
			}
			if !FileExists(filepath.Join(dir, name)) {
				continue
			}
			if c := filepath.Join(dir, rel); FileExists(c) {
				return c
			}
			if parent := filepath.Dir(dir); parent != "" {
				if c := filepath.Join(parent, rel); FileExists(c) {
					return c
				}
			}
		}
	}

	var roots []string
	if out, err := npmRootGlobal(); err == nil && out != "" {
		roots = append(roots, out)
	}
	roots = append(roots,
		filepath.Join(envOr("APPDATA", ""), "npm"),
		filepath.Join(envOr("ProgramFiles", `C:\Program Files`), "nodejs"),
		`D:\npm\node_global`,
	)
	for _, r := range roots {
		if r == "" {
			continue
		}
		if c := filepath.Join(r, rel); FileExists(c) {
			return c
		}
	}
	return ""
}

func npmRootGlobal() (string, error) {
	cmd := exec.Command("npm", "root", "-g")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, nil
		}
	}
	return "", nil
}
