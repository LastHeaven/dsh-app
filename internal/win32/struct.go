//go:build windows

package win32

// 这里每个结构体的字段顺序、宽度都直接映射到 C 内存布局。
// **改动等同于改 ABI**：任何一个字段错了，Windows 会读到撕裂的数据，
// 典型表现是 API 返回成功但行为诡异（托盘图标不响应就是其中之一）。

type Point struct {
	X int32
	Y int32
}

type Msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      Point
}

type WndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  uintptr
}

// NotifyIconData 对应 NOTIFYICONDATAW（Vista 以后的 976 字节布局）。
type NotifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GuidItem         [16]byte
	BalloonIcon      uintptr
}

type SecurityAttributes struct {
	Length             uint32
	SecurityDescriptor uintptr
	InheritHandle      bool
}

type UnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

// ProcessCommandLineInfo 是 NtQueryInformationProcess(class 60) 的输出结构。
// x64 布局：Length(0,u16) MaxLength(2,u16) pad(4) Buffer(8,ptr)
type ProcessCommandLineInfo struct {
	CommandLine UnicodeString
}

type ProcessEntry32 struct {
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

// KeybdInput / InputStruct 是 SendInput 的入参。
//
// INPUT 结构体的 x64 精确布局（**必须对，否则 SendInput 整批失败**）：
//
//	INPUT     = type(4) + pad(4) + union(32)  = 40 字节
//	KEYBDINPUT= wVk(2) wScan(2) dwFlags(4) time(4) dwExtraInfo(8) = 20 字节
//
// union 部分取最大成员 MOUSEINPUT(28) 对齐后的 32 字节。
// 最初把 KeybdInput 补到 32、INPUT 变成 56 字节，SendInput 因 cbSize
// 不对直接返回 0 —— 模拟 ALT 完全无效，菜单还是弹不出来（实测踩到）。
type KeybdInput struct {
	Vk    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	Extra uintptr
}

type InputStruct struct {
	Type uint32
	_    uint32
	Ki   KeybdInput // 24 字节（20 内容 + 4 对齐填充）
	_    [8]byte    // 补齐 union 到 32 字节 → 整体 40 字节
}
