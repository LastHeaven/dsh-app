//go:build windows

package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// ------------------------------------------------------------------ 路径

type pathsT struct{}

var Paths = pathsT{}

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
func (pathsT) Root() string {
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

func (p pathsT) LogDir() string  { return filepath.Join(p.Root(), "logs") }
func (p pathsT) OutLog() string  { return filepath.Join(p.LogDir(), "dsh-web.out.log") }
func (p pathsT) ErrLog() string  { return filepath.Join(p.LogDir(), "dsh-web.err.log") }
func (p pathsT) UrlFile() string { return filepath.Join(p.Root(), "last-url.txt") }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func safeRead(p string) string {
	if !fileExists(p) {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func tryDelete(p string) {
	if fileExists(p) {
		_ = os.Remove(p)
	}
}

// trimForMsg 截断长文本用于错误提示（和 C# 版 Paths.Trim 一致）
func trimForMsg(s string, max int) string {
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
func (pathsT) IsPortListening(port int) bool {
	return portOwnerPid(port) > 0
}

// OpenUrl 用系统默认浏览器打开地址。
//
// **不要用 exec.Command("explorer.exe", url)** —— 实测这条会让 explorer
// 打开「文档」文件夹而不是浏览器。原因：Go 的 exec.Command 会把参数按
// MS 转义规则拼成命令行，explorer.exe 对引号的处理很特别，带引号的 URL
// 被它当成"找不到的路径"，于是退化成打开默认文件夹。
// （加 HideWindow 能绕过，但那是碰巧改了命令行拼法，不可靠。）
//
// 正确做法是 ShellExecuteW：它就是 C# 版 UseShellExecute=true 背后的东西，
// 由 shell 按 URL 协议去找默认浏览器，且浏览器保持普通权限（不会被我们
// 的管理员身份带成提权进程）。
func (pathsT) OpenUrl(u string) {
	u = strings.TrimSpace(u)
	if u == "" {
		return
	}
	// 只放行 http/https，防止把奇怪字符串交给 shell
	low := strings.ToLower(u)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return
	}
	if shellExecuteOpen(u) {
		return
	}
	// 兜底：走 FileProtocolHandler
	_ = startDetached("rundll32.exe", []string{"url.dll,FileProtocolHandler", u})
}

// OpenFolder 打开资源管理器窗口。这里是**真的要打开文件夹**，
// 用 explorer.exe 是对的（而且必须带引号处理，见 startDetached 说明）。
func (pathsT) OpenFolder(p string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return
	}
	if !dirExists(p) {
		_ = os.MkdirAll(p, 0o755)
	}
	// 打开文件夹时这里不用 ShellExecuteW：给了路径就得开资源管理器，
	// 用 explorer.exe 语义最明确。
	_ = startDetached("explorer.exe", []string{p})
}

// startDetached 用 CreateProcess 起一个独立进程。
//
// **注意**：只用它启动「真的要开文件/文件夹」的程序（explorer、rundll32）。
// 不要用它打开网页 —— 见 OpenUrl 的说明。
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

// ResolveNode 找 node.exe：先扫 PATH，再试常见安装位置。
func (pathsT) ResolveNode() string {
	for _, raw := range strings.Split(os.Getenv("PATH"), ";") {
		dir := strings.Trim(strings.TrimSpace(raw), `"`)
		if dir == "" {
			continue
		}
		c := filepath.Join(dir, "node.exe")
		if fileExists(c) {
			return c
		}
	}
	cands := []string{
		filepath.Join(envOr("ProgramFiles", `C:\Program Files`), "nodejs", "node.exe"),
		filepath.Join(envOr("ProgramFiles(x86)", `C:\Program Files (x86)`), "nodejs", "node.exe"),
		filepath.Join(envOr("LOCALAPPDATA", ""), "Programs", "nodejs", "node.exe"),
	}
	for _, c := range cands {
		if fileExists(c) {
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
func (p pathsT) ResolveDshBin() string {
	const rel = `node_modules\@deepseek-ai\dsh\lib\bin.js`

	for _, name := range []string{"dsh.cmd", "dsh.ps1", "dsh.bat", "dsh.exe", "dsh"} {
		for _, raw := range strings.Split(os.Getenv("PATH"), ";") {
			dir := strings.Trim(strings.TrimSpace(raw), `"`)
			if dir == "" {
				continue
			}
			if !fileExists(filepath.Join(dir, name)) {
				continue
			}
			if c := filepath.Join(dir, rel); fileExists(c) {
				return c
			}
			if parent := filepath.Dir(dir); parent != "" {
				if c := filepath.Join(parent, rel); fileExists(c) {
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
		if c := filepath.Join(r, rel); fileExists(c) {
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

// urlPort 取地址里的端口，用于校验 last-url.txt。
func urlPort(raw string) (int, bool) {
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

// ------------------------------------------------------------------ 进程快照

// nodeProc 是一次性快照里的一条记录。
type nodeProc struct {
	PID    uint32
	Parent uint32
	Cmd    string
}

var (
	binPattern = regexp.MustCompile(`(?i)@deepseek-ai[\\/]dsh[\\/]lib[\\/]bin\.js`)
	webPattern = regexp.MustCompile(`(?i)(^|\s)web(\s|$)`)
	urlPattern = regexp.MustCompile(`https?://[^\s"']+`)
)

// 快照缓存：同一进程里反复枚举容易出问题，抓一次存着，需要新鲜的显式失效。
var (
	snapshot    []nodeProc
	snapshotSet bool
)

func invalidateSnapshot() {
	snapshot = nil
	snapshotSet = false
}

// Snapshot 枚举 node.exe 的 pid/父 pid/命令行。
//
// 命令行用 NtQueryInformationProcess(class 60) 拿。**不要再用 WMI**：
// 它依赖反射，原生编译/裁剪下直接崩（MissingMethodException: WbemDefPath），
// 而且要拖 System.Management 进来。
// 也不要退回读 PEB（NtQueryInformationProcess+ReadProcessMemory）——那条
// 在 node.exe 上返回 ERROR_ACCESS_DENIED(5)，历史上已证明走不通。
func snapshotProcs() []nodeProc {
	if snapshotSet {
		return snapshot
	}
	var list []nodeProc
	for _, pid := range enumNodePids() {
		cmd, err := commandLineOf(pid)
		if err != nil {
			// 拿不到命令行（权限/进程已退）就跳过：宁可漏判，也不能误判
			continue
		}
		list = append(list, nodeProc{PID: pid, Cmd: cmd})
	}
	snapshot = list
	snapshotSet = true
	return list
}

// commandLineOf 读指定进程的完整命令行。
func commandLineOf(pid uint32) (string, error) {
	h, err := openProcess(processQueryLimitedInformation, pid)
	if err != nil {
		return "", err
	}
	defer closeHandle(h)

	// 先探长度，再分配
	var needed uint32
	procNtQueryInformationProcess.Call(h, uintptr(processCommandLineInformation),
		0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 {
		return "", fmt.Errorf("no command line for pid %d", pid)
	}

	buf := make([]byte, needed+16)
	var got uint32
	status, _, _ := procNtQueryInformationProcess.Call(h, uintptr(processCommandLineInformation),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&got)))
	if status != 0 {
		return "", fmt.Errorf("NtQueryInformationProcess status 0x%x", status)
	}

	// 返回的是 PROCESS_COMMANDLINE_INFORMATION { UNICODE_STRING CommandLine }。
	// x64 布局：Length(0,u16) MaxLength(2,u16) pad(4) Buffer(8,ptr)
	length := *(*uint16)(unsafe.Pointer(&buf[0]))
	if length == 0 {
		return "", fmt.Errorf("empty command line for pid %d", pid)
	}
	ptr := *(*uintptr)(unsafe.Pointer(&buf[8]))
	if ptr == 0 {
		return "", fmt.Errorf("null command line buffer for pid %d", pid)
	}
	n := int(length) / 2
	if n <= 0 || n > 64*1024 {
		return "", fmt.Errorf("implausible command line length %d for pid %d", n, pid)
	}
	// 内核把命令行拷进了我们自己的 buf（Buffer 指向 buf 内部），
	// 所以直接从 buf 里按偏移读即可 —— 完全不需要把 uintptr 转回指针，
	// 也就不会触发 go vet 的 possible misuse of unsafe.Pointer。
	off := int(ptr - uintptr(unsafe.Pointer(&buf[0])))
	if off < 0 || off+n*2 > len(buf) {
		return "", fmt.Errorf("command line buffer out of range for pid %d", pid)
	}
	raw := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), n)
	return string(utf16.Decode(raw)), nil
}

// enumNodePids 用 Toolhelp 快照枚举 node.exe 的 pid。
func enumNodePids() []uint32 {
	const th32csSnapProcess = 0x00000002
	snap, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snap == invalidHandleValue || snap == 0 {
		return nil
	}
	defer closeHandle(snap)

	var e processEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	r, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&e)))
	if r == 0 {
		return nil
	}
	var out []uint32
	for {
		if strings.EqualFold(syscall.UTF16ToString(e.ExeFile[:]), "node.exe") {
			out = append(out, e.ProcessID)
		}
		e.Size = uint32(unsafe.Sizeof(e))
		r, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&e)))
		if r == 0 {
			break
		}
	}
	return out
}

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}
