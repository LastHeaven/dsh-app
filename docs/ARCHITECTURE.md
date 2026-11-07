# 架构

## 数据流

```
main()
 ├─ initConsole()                 抓父控制台，中文用 WriteConsoleW
 ├─ parseOptions(os.Args[1:])
 │
 ├─ -DryRun / -Stop / -Headless ─→ runHeadless()  【跑完即退，绝不碰托盘】
 │
 └─ 托盘模式
     ├─ acquireSingleInstance()   互斥体；抢不到就 forwardCommand() 转发后退
     ├─ setProcessDpiAwarenessContext(PerMonitorV2)
     ├─ newTrayApp() → Run()
     │    ├─ RegisterClassExW + CreateWindowExW(HWND_MESSAGE)  隐藏消息窗口
     │    ├─ Shell_NotifyIcon(NIM_ADD)
     │    ├─ SetTimer(poll, 3000ms) → 定期 refreshState()
     │    └─ GetMessage / DispatchMessage 循环
     └─ 命令来自两处：菜单(TPM_RETURNCMD) 和 命名管道（第二实例转发）
```

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `main.go` | 入口分流、`runHeadless`、`diagLog`、小工具函数 |
| `win32.go` | 全部 Win32 绑定：DLL/Proc、常量、结构体、薄封装 |
| `paths.go` | `Paths`（Root/日志路径/node 与 bin.js 查找/开网页开文件夹）、进程快照与命令行读取 |
| `service.go` | `Stop(port)`、`Start()`、`LastUrl(port)`、TCP 端口归属、`procListensOnPort` |
| `tray.go` | 托盘窗口与消息循环、右键菜单、三态显示、各动作的异步封装 |
| `single.go` | 互斥体 + 命名管道单实例转发 |
| `console.go` | 控制台 shim、`Options` 解析、帮助文本 |
| `icons.go` | 内嵌 ICO → 临时文件 → `LoadImageW` |

## 线程模型

**这里最容易出错：Win32 窗口有线程亲和性。**

| 线程 | 能做什么 |
| --- | --- |
| 主线程（消息循环） | 创建/销毁窗口、`Shell_NotifyIcon`、跑 `wndProc` |
| goroutine | 一切耗时操作：`Start()`、`Stop()`、打开网页/文件夹 |

`wndProc` 是回调，**绝不能在里面做耗时操作**，否则整个托盘卡死。
所以菜单命令都转成 `go t.xxxAsync()`。

反过来说，goroutine 里**不能**直接 `CreateWindow` / `DestroyWindow` /
`TrackPopupMenu`（有线程亲和性）。退出路径就是因此用
`PostMessage(WM_CLOSE)` 绕回主线程收尾，详见
[PITFALLS.md](PITFALLS.md#退出托盘要顺手停掉它管的服务)。

## 单实例机制

- 互斥体名 `Global\dsh-web-tray-{7E2F5C41-...}`，**和旧 C# 版一致**，
  避免新旧版本同时跑出现两个图标。
- 抢不到互斥体 → 连命名管道 `\\.\pipe\dsh-web-tray-{...}` 把命令行发过去，
  然后自己退出。发不出去就弹一个 MessageBox 提示"已在托盘中运行"。
- 主实例在后台 goroutine 里循环收管道消息 → `handleForwarded()`。

## 三态判定

托盘图标状态**只看端口有没有 LISTEN**，不看进程在不在：

| 状态 | 条件 | 图标 |
| --- | --- | --- |
| 运行中 | 端口 LISTEN | 黑鲸鱼 `#0B0B0F` |
| 未运行 | 端口没 LISTEN | 灰鲸鱼 `#9AA0AE` |
| 忙碌 | `busy` 标志为真（启停进行中） | 琥珀 `#D99B00` |

轮询每 3 秒一次。`_wasRunning` 用于检测"意外退出"并弹气泡提示；
手动停止时把 `wasRunning` 置 false，避免误报。

## 关键不变量

1. **Headless 路径不构造托盘、不启管道监听。** 早先 C# 版在无界面模式下也
   `new TrayApp(...)`，导致短暂闪出托盘图标、甚至走错启动分支。
2. **`Stop()` 只停本端口那一个。** 见 [PITFALLS.md](PITFALLS.md#-stop-的判定必须按命令行---port不能猜)。
3. **打开网页走 `ShellExecuteW`，打开文件夹走 `explorer.exe`。**

## 为什么零第三方依赖

本机 `proxy.golang.org` DNS 不可达，`go get` 会失败。所以所有 Win32 绑定
都手写在 `win32.go` 里，没有 `golang.org/x/sys/windows`，也没有任何
托盘库。加依赖前先确认网络能通。
