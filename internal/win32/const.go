//go:build windows

package win32

const (
	// 进程访问权限
	ProcessQueryLimitedInformation = 0x1000
	ProcessTerminate               = 0x0001

	// NtQueryInformationProcess 信息类
	ProcessCommandLineInformation = 60

	// 窗口消息
	WMQueryEndSession = 0x0011
	WMEndSession      = 0x0016
	WMDestroy         = 0x0002
	WMClose           = 0x0010
	WMRButtonUp       = 0x0205
	WMLButtonUp       = 0x0202
	WMTimer           = 0x0113
	WMApp             = 0x8000
	WMNull            = 0x0000
	WMCommand         = 0x0111

	// 自定义消息：后台更新检查完成（WM_APP+1，避开菜单的 WM_APP 回调）
	WMUpdateReady = 0x8001

	// 菜单
	MFString       = 0x0000
	MFSeparator    = 0x0800
	MFGrayed       = 0x0001
	TPMRightButton = 0x0002
	TPMReturnCmd   = 0x0100
	TPMNonotify    = 0x0080

	// Shell_NotifyIcon
	NIMAdd    = 0
	NIMModify = 1
	NIMDelete = 2

	NIFMessage = 0x01
	NIFIcon    = 0x02
	NIFTip     = 0x04
	NIFInfo    = 0x10

	NIIFInfo    = 0x01
	NIIFWarning = 0x02
	NIIFError   = 0x03

	// 窗口样式
	WSPopup = 0x80000000

	// 标准句柄
	StdOutputHandle = ^uintptr(11) // (DWORD)-11

	// 文件类型
	FileTypeChar = 0x0002

	// CreateProcess
	CreateNoWindow = 0x08000000

	// DPI
	DPIAwarenessPerMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT -4

	// LoadImage
	ImageIcon      = 1
	LRLoadFromFile = 0x00000010
	LRDefaultSize  = 0x00000040
	LRShared       = 0x00008000

	// ShellExecute
	SWShownormal = 1

	// MessageBox
	MBOK              = 0x00000000
	MBIconInformation = 0x00000040
	MBSetForeground   = 0x00010000

	// TCP 表
	AFInet              = 2
	TCPTableOwnerPidAll = 5

	// 错误码
	ErrorAlreadyExists = 183
	ErrorPipeConnected = 535

	// 命名管道
	//
	// 方向别搞反：服务端是**从客户端读命令**，所以服务端建 PIPE_ACCESS_INBOUND(1)；
	// 客户端写，用 GENERIC_WRITE。最初这里抄成了 PIPE_ACCESS_OUTBOUND(2)，
	// 服务端拿不到读权限，ConnectNamedPipe 之后读不出东西，转发静默失败、
	// 第二个实例既不退出也不提示。
	PipeAccessInbound  = 0x00000001
	OpenExisting       = 3
	GenericWrite       = 0x40000000
	InvalidHandleValue = ^uintptr(0)

	// WinHTTP
	//
	// WINHTTP_FLAG_SECURE：走 TLS。**漏了它 WinHTTP 会按明文 HTTP 发**，
	// 服务器直接回 "400 The plain HTTP request was sent to HTTPS port"。
	WinhttpAccessDefaultProxy = 0
	WinhttpFlagSecure         = 0x00800000

	// WINHTTP_QUERY_STATUS_CODE + WINHTTP_QUERY_FLAG_NUMBER：
	// 不加 NUMBER 标志拿到的不是 DWORD 状态码，永远读成 0。
	WinhttpQueryStatusCode = 19
	WinhttpQueryFlagNumber = 0x20000000
)
