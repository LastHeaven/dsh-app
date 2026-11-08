//go:build windows

// Package service 是 dsh web 服务进程的查询、启停与地址回收。
//
// 这个包里最需要小心的是 Stop —— 出过两次真实事故，
// 详见 Stop 的注释与 docs/PITFALLS.md。
package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LastHeaven/dsh-app/internal/config"
	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/procs"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

var (
	binPattern = regexp.MustCompile(`(?i)@deepseek-ai[\\/]dsh[\\/]lib[\\/]bin\.js`)
	webPattern = regexp.MustCompile(`(?i)(^|\s)web(\s|$)`)
	urlPattern = regexp.MustCompile(`https?://[^\s"']+`)
)

// DefaultPort 是 dsh web 不带 --port 时的默认端口（与 config.DefaultPort 一致）。
const DefaultPort = config.DefaultPort

// ---------------------------------------------------------------- 进程查询

// Live 返回所有「确实是 dsh web」的进程。
//
// 判定条件：node.exe 且命令行同时含
//   - @deepseek-ai/dsh/lib/bin.js
//   - 独立的 web 参数
//
// 两条都要满足，否则会误伤同一台机器上的 dsh CLI 会话。
func Live() []procs.Process {
	procs.Invalidate()
	var out []procs.Process
	for _, p := range procs.Snapshot() {
		if !binPattern.MatchString(p.Cmd) || !webPattern.MatchString(p.Cmd) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// listensOnPort 判断某个 dsh web 进程是不是跑在指定端口上。
//
// 依据命令行：`... bin.js web --port 3199 --no-open`。
// **没有 --port 参数时它跑在默认 3080 上** —— 这一点是早期事故的根源：
// 只按「web 参数」匹配会把不带 --port 的 3080 实例也当成候选，
// 于是 -Stop -Port 3199 在回落时把它停了。
func listensOnPort(cmd string, port int) bool {
	if p, ok := explicitPort(cmd); ok {
		return p == port
	}
	return port == DefaultPort
}

// explicitPort 从命令行里抽 `--port N` / `--port=N` / `-p N`。
func explicitPort(cmd string) (int, bool) {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		f := strings.ToLower(fields[i])
		switch {
		case f == "--port" || f == "-p":
			if i+1 < len(fields) {
				if n, ok := atoi(strings.Trim(fields[i+1], `"'`)); ok {
					return n, true
				}
			}
		case strings.HasPrefix(f, "--port="):
			if n, ok := atoi(strings.TrimPrefix(f, "--port=")); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func atoi(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0, false
		}
	}
	return n, true
}

// PortOwnerPid 返回监听该端口的进程 pid，没有则 0。
func PortOwnerPid(port int) uint32 {
	return win32.PortOwnerPid(port)
}

// ---------------------------------------------------------------- 停止

// Stop 停掉**指定端口上**的 dsh web，返回结束的进程数。
//
// **这是全项目最需要小心的地方，已经出过两次真实事故：**
//
//  1. 最初照搬 .ps1 的「杀掉所有 dsh web」语义，用 -Port 3199 隔离测试时
//     把用户正在用的 3080 一起杀了。
//  2. Go 重写时照搬了 C# 版的回落规则「端口无归属 + 全机只剩 1 个 web 就停它」，
//     结果 `-Stop -Port 3199`（3199 已经空了）又把 3080 停了 —— 因为那唯一的
//     一个正是 3080。回落规则本身就是错的方向。
//
// 现在的判定依据是**进程命令行里的 --port**，而不是「猜」：
//  1. 只停命令行里明确写了 `--port <port>` 的那个进程；
//  2. 没写 --port 的（默认 3080）只有在目标端口就是 3080 时才可能成为候选；
//  3. 端口归属 pid 与候选交叉验证，两者一致才动手；
//  4. **任何情况下都不存在「停掉唯一一个 web」这种回落**；
//  5. 只 Kill 目标 pid 本身，不递归杀子进程树
//     （早期递归 KillTree 因 PID 复用导致父子关系失真，把本进程也带走了）。
//
// 宁可「该停的没停」，也绝不「多停一个」。
func Stop(port int) int {
	live := Live()
	if len(live) == 0 {
		return 0
	}

	var targets []uint32
	owner := PortOwnerPid(port)

	for _, p := range live {
		if !listensOnPort(p.Cmd, port) {
			continue
		}
		// 命令行说它跑在 port 上。如果端口还有归属且对不上，说明不是它，
		// 别动 —— 避免 PID 复用之类的错配。
		if owner > 0 && owner != p.PID {
			continue
		}
		targets = append(targets, p.PID)
	}

	if len(targets) == 0 {
		return 0
	}

	for _, pid := range targets {
		if h, err := win32.OpenProcess(win32.ProcessTerminate, pid); err == nil {
			win32.TerminateProcess(h, 1)
			win32.CloseHandle(h)
		}
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, pid := range targets {
			if procs.Alive(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	// 还活着就再补一刀
	for _, pid := range targets {
		if procs.Alive(pid) {
			if h, err := win32.OpenProcess(win32.ProcessTerminate, pid); err == nil {
				win32.TerminateProcess(h, 1)
				win32.CloseHandle(h)
			}
		}
	}

	procs.Invalidate()
	return len(targets)
}

// ---------------------------------------------------------------- 启动

// captureWriter 边写日志边抓访问地址。
type captureWriter struct {
	mu sync.Mutex
	sb strings.Builder
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.sb.Write(p)
	// 只留有界内容，长时间运行不会无限涨
	if c.sb.Len() > 1<<20 {
		s := c.sb.String()
		c.sb.Reset()
		c.sb.WriteString(s[len(s)>>1:])
	}
	c.mu.Unlock()
	return len(p), nil
}

func (c *captureWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sb.String()
}

// Start 后台拉起 dsh web，返回带 token 的访问地址。
func Start(opt *config.Options) (string, error) {
	// 只有目标端口确实被占着时才停那一个实例，不影响其它端口
	if paths.IsPortListening(opt.Port) && !opt.KeepExisting {
		Stop(opt.Port)
	}

	node := paths.ResolveNode()
	if node == "" {
		return "", errors.New("未找到 node.exe，请确认 Node.js 已安装并加入 PATH。")
	}
	bin := paths.ResolveDshBin()
	if bin == "" {
		return "", errors.New("未找到 dsh 入口 bin.js，请确认已执行：npm i -g @deepseek-ai/dsh")
	}

	// 等端口腾出来
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}
	if paths.IsPortListening(opt.Port) {
		return "", fmt.Errorf("端口 %d 仍被其它程序占用。", opt.Port)
	}

	if err := os.MkdirAll(paths.LogDir(), 0o755); err != nil {
		return "", fmt.Errorf("创建日志目录失败：%w", err)
	}
	paths.TryDelete(paths.OutLog())
	paths.TryDelete(paths.ErrLog())

	outFile, err := os.Create(paths.OutLog())
	if err != nil {
		return "", fmt.Errorf("创建日志文件失败：%w", err)
	}
	cap := &captureWriter{}
	writer := io.MultiWriter(outFile, cap)

	cmd := exec.Command(node, bin, "web", "--port", fmt.Sprint(opt.Port), "--no-open")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: win32.CreateNoWindow,
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		outFile.Close()
		return "", fmt.Errorf("无法创建 node 进程：%w", err)
	}

	exitCh := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		exitCh <- code
	}()

	// dsh web 启动时会打印带 token 的访问地址，从这里抓
	var url string
	deadline = time.Now().Add(120 * time.Second)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	var exitedCode int
	exited := false
waitLoop:
	for time.Now().Before(deadline) {
		select {
		case code := <-exitCh:
			exited, exitedCode = true, code
			break waitLoop
		case <-ticker.C:
			if m := urlPattern.FindString(cap.String()); m != "" {
				url = m
				break waitLoop
			}
		}
	}

	outFile.Close()

	if url == "" && !exited {
		// 再给最后一次机会：进程可能刚打印完
		if m := urlPattern.FindString(cap.String()); m != "" {
			url = m
		}
	}

	if exited && url == "" {
		o := paths.SafeRead(paths.OutLog())
		e := paths.SafeRead(paths.ErrLog())
		return "", fmt.Errorf("dsh web 启动后立即退出（退出码 %d）：%s",
			exitedCode, paths.TrimForMsg(o+"\n"+e, 300))
	}
	if url == "" {
		return "", errors.New("等待 dsh web 输出访问地址超时，请查看 logs 目录。")
	}

	// 等端口真的起来
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}

	if err := os.WriteFile(paths.UrlFile(), []byte(url), 0o644); err != nil {
		_ = err
	}

	if !opt.NoBrowser {
		paths.OpenURL(url)
	}
	return url, nil
}

// LastURL 取该端口最近一次的访问地址。
//
// 必须校验端口：last-url.txt 是历史遗留文件，调试时用 -Port 3199 起过实例
// 就会把 3199 的地址写进去。不校验的话，托盘会打开一个早已失效的端口，
// 表现为「点开页面却是 3199」。
func LastURL(port int) string {
	if f := paths.UrlFile(); paths.FileExists(f) {
		last := strings.TrimSpace(paths.SafeRead(f))
		if p, ok := paths.URLPort(last); ok && p == port {
			return last
		}
	}

	// 文件没写或端口不符，从日志里倒序找该端口的最后一个地址
	if logPath := paths.OutLog(); paths.FileExists(logPath) {
		all := urlPattern.FindAllString(paths.SafeRead(logPath), -1)
		for i := len(all) - 1; i >= 0; i-- {
			if p, ok := paths.URLPort(all[i]); ok && p == port {
				return all[i]
			}
		}
	}
	return ""
}
