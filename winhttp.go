//go:build windows

// WinHTTP 绑定 —— 检测更新用的最小 HTTPS 客户端。
//
// **不要改用 net/http**：它会把 http2、DNS resolver、reflect 整条链拉进来，
// 实测让二进制从 3.0 MB 涨到 5.7 MB，把当初重写省下的体积又还回去一半。
// winhttp.dll 是系统组件，走 P/Invoke 只涨约 0.1 MB，且仍然是零第三方依赖。
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var modwinhttp = syscall.NewLazyDLL("winhttp.dll")

var (
	procWinHttpOpen        = modwinhttp.NewProc("WinHttpOpen")
	procWinHttpConnect     = modwinhttp.NewProc("WinHttpConnect")
	procWinHttpOpenRequest = modwinhttp.NewProc("WinHttpOpenRequest")
	procWinHttpSetTimeouts = modwinhttp.NewProc("WinHttpSetTimeouts")
	procWinHttpSendRequest = modwinhttp.NewProc("WinHttpSendRequest")
	procWinHttpReceiveResp = modwinhttp.NewProc("WinHttpReceiveResponse")
	procWinHttpQueryHdrs   = modwinhttp.NewProc("WinHttpQueryHeaders")
	procWinHttpReadData    = modwinhttp.NewProc("WinHttpReadData")
	procWinHttpCloseHandle = modwinhttp.NewProc("WinHttpCloseHandle")
)

const (
	// WINHTTP_ACCESS_TYPE_DEFAULT_PROXY：跟随系统代理设置
	winhttpAccessDefaultProxy = 0

	// WINHTTP_FLAG_SECURE：走 TLS。**漏了它 WinHTTP 会按明文 HTTP 发**，
	// 服务器直接回 "400 The plain HTTP request was sent to HTTPS port"。
	winhttpFlagSecure = 0x00800000

	// WINHTTP_QUERY_STATUS_CODE + WINHTTP_QUERY_FLAG_NUMBER：
	// 不加 NUMBER 标志拿到的不是 DWORD 状态码，永远读成 0。
	winhttpQueryStatusCode = 19
	winhttpQueryFlagNumber = 0x20000000

	// 响应体上限：dist-tags 只有 67 字节，给 1 MB 已经非常宽松
	maxHTTPBodyBytes = 1 << 20
)

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

// diskFreeBytes 返回路径所在磁盘的可用字节数。
func diskFreeBytes(path string) (uint64, error) {
	p := utf16Ptr(path)
	if p == nil {
		return 0, errors.New("bad path")
	}
	var free, total, avail uint64
	r, _, err := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0, err
	}
	return avail, nil
}

// winhttpGet 发一个 GET，返回状态码和响应体。
//
// 全程设超时：没有超时的网络调用在托盘程序里等于线程泄漏
// （DNS 卡住时 goroutine 会挂几个小时）。
func winhttpGet(rawURL string, timeoutMs int) (int, string, error) {
	scheme, host, port, path, err := splitHTTPURL(rawURL)
	if err != nil {
		return 0, "", err
	}

	flags := uintptr(0)
	if scheme == "https" {
		flags = winhttpFlagSecure
	}

	agent := utf16Ptr("dsh-web-tray/1.0")
	sess, _, callErr := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		uintptr(winhttpAccessDefaultProxy), 0, 0, 0)
	if sess == 0 {
		return 0, "", fmt.Errorf("WinHttpOpen 失败：%v", callErr)
	}
	defer procWinHttpCloseHandle.Call(sess)

	procWinHttpSetTimeouts.Call(sess,
		uintptr(timeoutMs), uintptr(timeoutMs), uintptr(timeoutMs), uintptr(timeoutMs))

	conn, _, callErr := procWinHttpConnect.Call(
		sess, uintptr(unsafe.Pointer(utf16Ptr(host))), uintptr(port), 0)
	if conn == 0 {
		return 0, "", fmt.Errorf("无法连接 %s：%v", host, callErr)
	}
	defer procWinHttpCloseHandle.Call(conn)

	req, _, callErr := procWinHttpOpenRequest.Call(conn,
		uintptr(unsafe.Pointer(utf16Ptr("GET"))),
		uintptr(unsafe.Pointer(utf16Ptr(path))),
		0, 0, 0, flags)
	if req == 0 {
		return 0, "", fmt.Errorf("WinHttpOpenRequest 失败：%v", callErr)
	}
	defer procWinHttpCloseHandle.Call(req)

	if ok, _, callErr := procWinHttpSendRequest.Call(req, 0, 0, 0, 0, 0, 0); ok == 0 {
		return 0, "", fmt.Errorf("请求发送失败（网络不通？）：%v", callErr)
	}
	if ok, _, callErr := procWinHttpReceiveResp.Call(req, 0); ok == 0 {
		return 0, "", fmt.Errorf("没有收到响应：%v", callErr)
	}

	var status uint32
	ln := uint32(4)
	procWinHttpQueryHdrs.Call(req,
		uintptr(winhttpQueryStatusCode|winhttpQueryFlagNumber),
		0,
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&ln)), 0)

	var body []byte
	buf := make([]byte, 8192)
	for len(body) < maxHTTPBodyBytes {
		var n uint32
		ok, _, _ := procWinHttpReadData.Call(req,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unsafe.Pointer(&n)))
		if ok == 0 || n == 0 {
			break
		}
		body = append(body, buf[:n]...)
	}
	return int(status), string(body), nil
}
