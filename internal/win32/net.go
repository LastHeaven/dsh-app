//go:build windows

package win32

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

// ---------------------------------------------------------------- TCP 表

// TCPRowFor 找指定端口对应的 pid。listenOnly=true 只认 LISTEN 状态。
//
// 坑：端口号在 dwLocalPort 里是**高字节序存在低 16 位**，必须换字节序再比。
// 表类型用 TCP_TABLE_OWNER_PID_ALL(=5)，不是 ..._LISTENER(=3)。
// 行布局（24 字节）：
//
//	dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
//	dwRemotePort(16) dwOwningPid(20)
func TCPRowFor(port int, listenOnly bool) (uint32, bool) {
	var size uint32
	ProcGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, AFInet, TCPTableOwnerPidAll, 0)
	if size == 0 {
		return 0, false
	}
	buf := make([]byte, size)
	r, _, _ := ProcGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, AFInet, TCPTableOwnerPidAll, 0)
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

// PortOwnerPid 返回监听该端口的进程 pid，没有则 0。
func PortOwnerPid(port int) uint32 {
	if pid, ok := TCPRowFor(port, true); ok && pid > 0 {
		return pid
	}
	pid, _ := TCPRowFor(port, false)
	return pid
}

// ---------------------------------------------------------------- WinHTTP

// 响应体上限：dist-tags 只有 67 字节，给 1 MB 已经非常宽松
const maxHTTPBodyBytes = 1 << 20

// splitHTTPURL 把 URL 拆成交给 WinHTTP 的几段。
func splitHTTPURL(raw string) (scheme, host string, port int, path string, err error) {
	raw = strings.TrimSpace(raw)
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", "", 0, "", fmt.Errorf("地址缺少协议：%s", raw)
	}
	scheme = strings.ToLower(raw[:i])
	if scheme != "http" && scheme != "https" {
		return "", "", 0, "", fmt.Errorf("不支持的协议：%s", scheme)
	}
	rest := raw[i+3:]

	if slash := strings.IndexByte(rest, '/'); slash < 0 {
		host, path = rest, "/"
	} else {
		host, path = rest[:slash], rest[slash:]
	}

	port = 443
	if scheme == "http" {
		port = 80
	}
	if c := strings.LastIndexByte(host, ':'); c >= 0 {
		if n, e := strconv.Atoi(host[c+1:]); e == nil && n > 0 && n <= 65535 {
			port = n
			host = host[:c]
		}
	}
	if host == "" {
		return "", "", 0, "", errors.New("地址缺少主机名")
	}
	return scheme, host, port, path, nil
}

// SplitHTTPURL 导出给测试和调用方做地址校验用。
func SplitHTTPURL(raw string) (scheme, host string, port int, path string, err error) {
	return splitHTTPURL(raw)
}

// WinHTTPGet 发一个 GET，返回状态码和响应体。
//
// **不要改用 net/http**：它会把 http2、DNS resolver、reflect 整条链拉进来，
// 实测让二进制从 3.0 MB 涨到 5.7 MB，把当初重写省下的体积又还回去一半。
// winhttp.dll 是系统组件，走 P/Invoke 只涨约 0.1 MB，且仍然是零第三方依赖。
//
// 全程设超时：没有超时的网络调用在托盘程序里等于线程泄漏
// （DNS 卡住时 goroutine 会挂几个小时）。
func WinHTTPGet(rawURL string, timeoutMs int) (int, string, error) {
	scheme, host, port, path, err := splitHTTPURL(rawURL)
	if err != nil {
		return 0, "", err
	}

	flags := uintptr(0)
	if scheme == "https" {
		flags = WinhttpFlagSecure
	}

	agent := UTF16Ptr("dsh-web-tray/1.0")
	hostP := UTF16Ptr(host)
	verb := UTF16Ptr("GET")
	pathP := UTF16Ptr(path)
	defer runtime.KeepAlive(agent)
	defer runtime.KeepAlive(hostP)
	defer runtime.KeepAlive(verb)
	defer runtime.KeepAlive(pathP)

	sess, _, callErr := ProcWinHttpOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		uintptr(WinhttpAccessDefaultProxy), 0, 0, 0)
	if sess == 0 {
		return 0, "", fmt.Errorf("WinHttpOpen 失败：%v", callErr)
	}
	defer ProcWinHttpCloseHandle.Call(sess)

	ProcWinHttpSetTimeouts.Call(sess,
		uintptr(timeoutMs), uintptr(timeoutMs), uintptr(timeoutMs), uintptr(timeoutMs))

	conn, _, callErr := ProcWinHttpConnect.Call(
		sess, uintptr(unsafe.Pointer(hostP)), uintptr(port), 0)
	if conn == 0 {
		return 0, "", fmt.Errorf("无法连接 %s：%v", host, callErr)
	}
	defer ProcWinHttpCloseHandle.Call(conn)

	req, _, callErr := ProcWinHttpOpenRequest.Call(conn,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(pathP)),
		0, 0, 0, flags)
	if req == 0 {
		return 0, "", fmt.Errorf("WinHttpOpenRequest 失败：%v", callErr)
	}
	defer ProcWinHttpCloseHandle.Call(req)

	if ok, _, callErr := ProcWinHttpSendRequest.Call(req, 0, 0, 0, 0, 0, 0); ok == 0 {
		return 0, "", fmt.Errorf("请求发送失败（网络不通？）：%v", callErr)
	}
	if ok, _, callErr := ProcWinHttpReceiveResp.Call(req, 0); ok == 0 {
		return 0, "", fmt.Errorf("没有收到响应：%v", callErr)
	}

	var status uint32
	ln := uint32(4)
	ProcWinHttpQueryHdrs.Call(req,
		uintptr(WinhttpQueryStatusCode|WinhttpQueryFlagNumber),
		0,
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&ln)), 0)

	var body []byte
	buf := make([]byte, 8192)
	for len(body) < maxHTTPBodyBytes {
		var n uint32
		ok, _, _ := ProcWinHttpReadData.Call(req,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unsafe.Pointer(&n)))
		if ok == 0 || n == 0 {
			break
		}
		body = append(body, buf[:n]...)
	}
	return int(status), string(body), nil
}
