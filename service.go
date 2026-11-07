//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ------------------------------------------------------------------ 进程查询

// liveWeb 返回所有「确实是 dsh web」的进程。
//
// 判定条件：node.exe 且命令行同时含
//   - @deepseek-ai/dsh/lib/bin.js
//   - 独立的 web 参数
//
// 两条都要满足，否则会误伤同一台机器上的 dsh CLI 会话。
func liveWeb() []nodeProc {
	invalidateSnapshot()
	var out []nodeProc
	for _, p := range snapshotProcs() {
		if !binPattern.MatchString(p.Cmd) || !webPattern.MatchString(p.Cmd) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// defaultPort 是 dsh web 不带 --port 时的默认端口。
const defaultPort = 3080

// procListensOnPort 判断某个 dsh web 进程是不是跑在指定端口上。
//
// 依据命令行：`... bin.js web --port 3199 --no-open`。
// **没有 --port 参数时它跑在默认 3080 上** —— 这一点是早期事故的根源：
// 只按「web 参数」匹配会把不带 --port 的 3080 实例也当成候选，
// 于是 -Stop -Port 3199 在回落时把它停了。
func procListensOnPort(cmd string, port int) bool {
	if p, ok := explicitPort(cmd); ok {
		return p == port
	}
	return port == defaultPort
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

// portOwnerPid 返回监听该端口的进程 pid，没有则 0。
//
// 坑：端口号在 dwLocalPort 里是**高字节序存在低 16 位**，必须换字节序再比。
// 表类型用 TCP_TABLE_OWNER_PID_ALL(=5)，不是 ..._LISTENER(=3)。
// 行布局（24 字节）：
//
//	dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
//	dwRemotePort(16) dwOwningPid(20)
func portOwnerPid(port int) uint32 {
	pid, _ := tcpRowFor(port, true)
	if pid > 0 {
		return pid
	}
	pid, _ = tcpRowFor(port, false)
	return pid
}

// tcpRowFor 找指定端口对应的 pid。listenOnly=true 只认 LISTEN 状态。
func tcpRowFor(port int, listenOnly bool) (uint32, bool) {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidAll, 0)
	if size == 0 {
		return 0, false
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, afInet, tcpTableOwnerPidAll, 0)
	if r != 0 {
		return 0, false
	}
	if len(buf) < 4 {
		return 0, false
	}

	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	const rowSize = 24
	for i := uint32(0); i < count; i++ {
		off := 4 + int(i)*rowSize
		if off+rowSize > len(buf) {
			break
		}
		state := *(*uint32)(unsafe.Pointer(&buf[off+0]))
		rawPort := *(*uint32)(unsafe.Pointer(&buf[off+8]))
		// 高字节序存在低 16 位，先换字节序
		localPort := int((rawPort&0xFF)<<8 | (rawPort>>8)&0xFF)
		if localPort != port {
			continue
		}
		if listenOnly && state != 2 { // 2 = MIB_TCP_STATE_LISTEN
			continue
		}
		owner := *(*uint32)(unsafe.Pointer(&buf[off+20]))
		if owner > 0 {
			return owner, true
		}
	}
	return 0, false
}

func pidAlive(pid uint32) bool {
	h, err := openProcess(processQueryLimitedInformation, pid)
	if err != nil {
		return false
	}
	defer closeHandle(h)
	// 已退出的进程句柄会立即 signaled
	const waitTimeout = 0
	return waitForSingleObject(h, waitTimeout) != 0 // WAIT_TIMEOUT(258) = 还活着
}

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
	live := liveWeb()
	if len(live) == 0 {
		return 0
	}

	var targets []uint32
	owner := portOwnerPid(port)

	for _, p := range live {
		if !procListensOnPort(p.Cmd, port) {
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
		if h, err := openProcess(processTerminate, pid); err == nil {
			terminateProcess(h, 1)
			closeHandle(h)
		}
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, pid := range targets {
			if pidAlive(pid) {
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
		if pidAlive(pid) {
			if h, err := openProcess(processTerminate, pid); err == nil {
				terminateProcess(h, 1)
				closeHandle(h)
			}
		}
	}

	invalidateSnapshot()
	return len(targets)
}

// ------------------------------------------------------------------ 启动

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
func Start(opt *Options) (string, error) {
	// 只有目标端口确实被占着时才停那一个实例，不影响其它端口
	if Paths.IsPortListening(opt.Port) && !opt.KeepExisting {
		Stop(opt.Port)
	}

	node := Paths.ResolveNode()
	if node == "" {
		return "", errors.New("未找到 node.exe，请确认 Node.js 已安装并加入 PATH。")
	}
	bin := Paths.ResolveDshBin()
	if bin == "" {
		return "", errors.New("未找到 dsh 入口 bin.js，请确认已执行：npm i -g @deepseek-ai/dsh")
	}

	// 等端口腾出来
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && Paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}
	if Paths.IsPortListening(opt.Port) {
		return "", fmt.Errorf("端口 %d 仍被其它程序占用。", opt.Port)
	}

	if err := os.MkdirAll(Paths.LogDir(), 0o755); err != nil {
		return "", fmt.Errorf("创建日志目录失败：%w", err)
	}
	tryDelete(Paths.OutLog())
	tryDelete(Paths.ErrLog())

	outFile, err := os.Create(Paths.OutLog())
	if err != nil {
		return "", fmt.Errorf("创建日志文件失败：%w", err)
	}
	cap := &captureWriter{}
	writer := io.MultiWriter(outFile, cap)

	cmd := exec.Command(node, bin, "web", "--port", fmt.Sprint(opt.Port), "--no-open")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
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
		o := safeRead(Paths.OutLog())
		e := safeRead(Paths.ErrLog())
		return "", fmt.Errorf("dsh web 启动后立即退出（退出码 %d）：%s",
			exitedCode, trimForMsg(o+"\n"+e, 300))
	}
	if url == "" {
		return "", errors.New("等待 dsh web 输出访问地址超时，请查看 logs 目录。")
	}

	// 等端口真的起来
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !Paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}

	if err := os.WriteFile(Paths.UrlFile(), []byte(url), 0o644); err != nil {
		_ = err
	}

	if !opt.NoBrowser {
		Paths.OpenUrl(url)
	}
	return url, nil
}

// LastUrl 取该端口最近一次的访问地址。
//
// 必须校验端口：last-url.txt 是历史遗留文件，调试时用 -Port 3199 起过实例
// 就会把 3199 的地址写进去。不校验的话，托盘会打开一个早已失效的端口，
// 表现为「点开页面却是 3199」。
func LastUrl(port int) string {
	if f := Paths.UrlFile(); fileExists(f) {
		last := strings.TrimSpace(safeRead(f))
		if p, ok := urlPort(last); ok && p == port {
			return last
		}
	}

	// 文件没写或端口不符，从日志里倒序找该端口的最后一个地址
	if logPath := Paths.OutLog(); fileExists(logPath) {
		all := urlPattern.FindAllString(safeRead(logPath), -1)
		for i := len(all) - 1; i >= 0; i-- {
			if p, ok := urlPort(all[i]); ok && p == port {
				return all[i]
			}
		}
	}
	return ""
}

// readFileName 仅用于诊断
func readFileName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}
