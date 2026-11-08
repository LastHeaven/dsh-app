# 架构

## 目录结构

```
cmd/dsh-web/          程序入口（main 包）：只做命令行分流
internal/win32/       Win32 P/Invoke 绑定（唯一直接调系统 API 的地方）
internal/config/      共享的 Options 结构（打破包间循环依赖）
internal/paths/       运行期路径、文件小工具、node/dsh 定位、开网页开文件夹
internal/procs/       node.exe 进程快照与命令行读取
internal/service/     dsh web 的启停、端口归属、访问地址回收
internal/updater/     版本检测、dist-tags、两阶段升级与回滚、手写 JSON
internal/single/      互斥体 + 命名管道单实例转发
internal/applog/      三条运行期日志（诊断 / 更新 / Headless）
internal/cli/         控制台输出（WriteConsoleW）与参数解析
internal/tray/        通知区图标、消息循环、菜单、看门狗、三态显示
build/                构建输入：windres 资源、exe 图标、图标生成脚本
docs/                 设计与踩坑记录
```

**依赖方向是单向的**，不允许出现环：

```
cmd/dsh-web ─→ cli, config, service, single, tray, updater, applog, win32
tray        ─→ cli, config, paths, service, updater, applog, win32
updater     ─→ config, paths, service, applog, win32
service     ─→ config, paths, procs, win32
procs       ─→ win32
paths       ─→ win32
applog      ─→ paths
cli         ─→ config, applog, win32
```

`internal/config` 是最底层的数据定义，谁都可以依赖它，它谁也不依赖 ——
这是当初 `Options` 放在 `cli` 里会逼出 `service → cli → service` 循环的原因。

## 数据流

```
main()
 ├─ cli.Init()                    抓父控制台，中文用 WriteConsoleW
 ├─ cli.ParseOptions(os.Args[1:])
 │
 ├─ -DryRun / -Stop / -Headless ─→ runHeadless()  【跑完即退，绝不碰托盘】
 │
 └─ 托盘模式
     ├─ single.Acquire()          互斥体；抢不到就 single.Forward() 转发后退
     ├─ runTray()
     │    ├─ runtime.LockOSThread()          ← 托盘的命门，见线程模型
     │    ├─ win32.SetProcessDpiAwarenessContext(PerMonitorV2)
     │    ├─ tray.New() → Run()
     │    │    ├─ RegisterClassExW + CreateWindowExW(WS_POPUP, 顶层不可见窗口)
     │    │    │    └─ **不能用 HWND_MESSAGE**：收不到 TaskbarCreated 广播
     │    │    ├─ Shell_NotifyIcon(NIM_ADD)
     │    │    ├─ registerWindowMessage("TaskbarCreated") → explorer 重启时重建图标
     │    │    ├─ SetTimer(poll, 3000ms) → 状态轮询
     │    │    ├─ SetTimer(iconCheck, 10000ms) → 图标健康检查（看 NIM_MODIFY 返回值）
     │    │    ├─ go updateLoop()      延迟 90s 起，每 6 小时查一次新版本
     │    │    │    └─ 结果 postMessage(WMUpdateReady) 回窗口线程
     │    │    └─ GetMessage / DispatchMessage 循环
     │    │         └─ 结束后 main 里 os.Exit(0)，不留"无窗口空壳"
     │    └─ 命令来自两处：菜单(TPM_RETURNCMD) 和 命名管道（第二实例转发）
```

另有三条更新命令（同样跑完即退，不构造托盘）：

```
-CheckUpdate   → runCheckUpdate()     查一次，打印结果
-Update        → runUpdateCommand()   升到 latest
-UpdateTo x.y  → runUpdateCommand()   升到指定版本
```

## 文件职责

### cmd/dsh-web

| 文件 | 职责 |
| --- | --- |
| `main.go` | 入口分流、`runTray`、`runHeadless`、更新命令 |

### internal/win32

全部 Win32 绑定集中在这里，其它包不直接碰 `syscall`。

| 文件 | 职责 |
| --- | --- |
| `dll.go` | DLL 与 Proc 句柄（kernel32/user32/shell32/ntdll/iphlpapi/gdi32/winhttp） |
| `const.go` | 常量：窗口消息、菜单、通知区、进程权限、管道、WinHTTP |
| `struct.go` | 传给 Windows 的结构体，**改动等同改 ABI** |
| `util.go` | UTF-16 转换（含 KeepAlive 纪律）、last-error |
| `kernel.go` | 进程、句柄、互斥体、Toolhelp 快照、命令行读取、磁盘空间 |
| `user.go` | 窗口注册/创建/销毁、消息循环、置前、SendInput、MessageBox |
| `menu.go` | 弹出菜单（含分隔符必须传 NULL 的坑） |
| `icon.go` | 图标加载、DPI |
| `notify.go` | `Shell_NotifyIcon`、`ShellExecuteW` |
| `net.go` | TCP 端口归属、WinHTTP GET、URL 拆分 |

### 其余包

| 包 | 职责 |
| --- | --- |
| `config` | `Options` 与 `DefaultPort`，最底层的共享数据 |
| `paths` | `Root()`/日志路径、`ResolveNode`、`ResolveDshBin`、`OpenURL`/`OpenFolder` |
| `procs` | 带缓存的 node 进程快照、`Alive(pid)` |
| `service` | `Stop(port)`、`Start()`、`LastURL(port)`、`Live()` |
| `updater` | `Check()`、`Run()`、两阶段升级与回滚、`JSONTopString`、SemVer |
| `single` | 互斥体 + 命名管道单实例转发 |
| `applog` | `Diag`（tray-diag.log）、`UpdateLog`（logs\update.log）、`Headless`（headless.log） |
| `cli` | `Init`/`Say`、`ParseOptions`、`PrintHelp` |
| `tray` | `App`：窗口与消息循环、右键菜单、三态、看门狗、更新动作 |

`tray` 内部按主题再分文件：

| 文件 | 职责 |
| --- | --- |
| `tray.go` | `App` 结构、常量、embed 的图标集合、心跳变量 |
| `app.go` | `Run`、图标增删、消息循环、`wndProc` |
| `menu.go` | 右键菜单与命令分发 |
| `state.go` | `nidMu`、`setIcon`/`setText`/`balloon`、`refreshState` |
| `actions.go` | 启停/重启/打开/升级等异步动作、看门狗、`exit` |
| `update.go` | 后台更新循环、`WMUpdateReady` 处理、转发命令 |
| `icons.go` | `go:embed` 的 ICO → 临时文件 → `LoadImageW` |
| `types.go` | 指向 `win32`/`updater` 类型的别名，少写包名前缀 |

更新的完整设计见 [UPDATE.md](UPDATE.md)。

## 线程模型

**这里最容易出错：Win32 窗口有线程亲和性。**

| 线程 | 能做什么 |
| --- | --- |
| 主线程（消息循环） | 创建/销毁窗口、`Shell_NotifyIcon`、跑 `wndProc` |
| goroutine | 一切耗时操作：`Start()`、`Stop()`、打开网页/文件夹、更新检查与升级 |

**main 里必须 `runtime.LockOSThread()`。** Go 的 goroutine 会在 OS 线程间迁移，
而窗口、定时器、消息队列都绑定在创建它们的线程上。一旦迁移，`GetMessage`
就在错误的线程上等消息，表现成"图标还在、点了没反应"。
症状极具欺骗性：进程 `Responding` 仍为 True，栈上看着卡在正常的空闲等待。

`wndProc` 是回调，**绝不能在里面做耗时操作**（包括联网），否则整个托盘卡死。
所以菜单命令都转成 `go t.xxxAsync()`；后台检查更新也走 goroutine，
结果用 `PostMessage(WMUpdateReady)` 交回窗口线程再动菜单和气泡。

反过来说，goroutine 里**不能**直接 `CreateWindow` / `DestroyWindow` /
`TrackPopupMenu`（有线程亲和性）。退出路径就是因此用
`PostMessage(WM_CLOSE)` 绕回主线程收尾，详见
[PITFALLS.md](PITFALLS.md#退出托盘要顺手停掉它管的服务)。

## 托盘状态与并发

`App` 的字段按并发方式分三类，**加字段前先决定它属于哪一类**：

| 类别 | 字段 | 同步手段 |
| --- | --- | --- |
| 仅 UI 线程 | `hwnd`、`nid`、`icons`、`taskbarCreatedMsg` | `nid` 另受 `nidMu` 保护（UI 线程只用 `TryLock`） |
| 跨 goroutine 的布尔标志 | `busy`、`exiting`、`updating` | `atomic.Bool`，进状态用 `CompareAndSwap` |
| 普通共享数据 | `url`、`upd`、`notifiedV`、`wasRunning` | `stateMu sync.RWMutex` |

**为什么 `busy` 必须是原子 CAS**：菜单、管道转发、`openUIAsync` 三条路径
都会调 `startAsync`。先查后置的写法会让两个 goroutine 同时通过
`if busy` 检查、各自跑一遍 `Start()`，两个 node 抢同一个端口。
这是 `-race` 可复现的真实竞争，不是理论风险。

访问一律走访问器（`markBusy`/`isBusy`/`getURL`/`setUpdateState`/`noteNotified`…），
不要直接碰字段 —— 字段本身已不导出到这层之外能随手改的形态，
写错会在编译期被挡住。

**注意 `stateMu` 与 `nidMu` 是两把不同的锁，不能合并**：`nidMu` 有一条硬约定
"UI 线程绝不阻塞等锁"，混进普通读写会把那条纪律破坏掉。

## 关机/注销时的收尾

`wndProc` 处理 `WM_QUERYENDSESSION` 和 `WM_ENDSESSION`：收到就投一个
`go exit()`，让正常退出流程（含 `Stop()`）跑起来。

- 消息处理本身**必须极轻**：不能同步 `Stop()`，那会把关机拖住，
  系统可能直接强杀。
- `WM_QUERYENDSESSION` 返回 **0**（不阻止关机）。不能返回 1 ——
  那是"我正在保存数据请等我"，我们等的却是外部进程退出，处理不好会让关机卡几分钟。
- `exit()` 内部有 `beginExit()` 的 CAS，两条消息 + 菜单退出重复触达都安全。

## 单实例机制

- 互斥体名 `Global\dsh-web-tray-{7E2F5C41-...}`，**和旧 C# 版一致**，
  避免新旧版本同时跑出现两个图标。
- 抢不到互斥体 → 连命名管道 `\\.\pipe\dsh-web-tray-{...}` 把命令行发过去，
  然后自己退出。发不出去就弹一个 MessageBox 提示"已在托盘中运行"。
- 主实例在后台 goroutine 里循环收管道消息 → `tray.HandleForwarded()`。

## 三态判定

托盘图标状态**只看端口有没有 LISTEN**，不看进程在不在：

| 状态 | 条件 | 图标 |
| --- | --- | --- |
| 运行中 | 端口 LISTEN | 黑鲸鱼 `#0B0B0F` |
| 未运行 | 端口没 LISTEN | 灰鲸鱼 `#9AA0AE` |
| 忙碌 | `busy` 为真（启停/升级进行中） | 琥珀 `#D99B00` |

轮询每 3 秒一次。`wasRunning` 用于检测"意外退出"并弹气泡提示；
手动停止时把 `wasRunning` 置 false，避免误报。

## 启动超时

| 场景 | 等地址 | 说明 |
| --- | --- | --- |
| 托盘 | 60 秒 | 实测启动约 8 秒，60 秒是 7 倍余量 |
| 命令行（`-Headless`/`-Update`） | 120 秒 | 没有托盘、用户在等结果，多等一会儿比提前失败有用 |

等端口 LISTEN 另算 30 秒。原来托盘也用 120 秒，但一旦 node 卡住，
菜单里"正在启动"会挂两分半、期间再点也没反应（`busy` 挡着），体验很差。

## 资源与图标

两套图标**不能混**：

| 用途 | 位置 | 特征 |
| --- | --- | --- |
| 托盘三态 | `internal/tray/icons/*.ico`（go:embed） | 透明底，叠在任务栏上 |
| exe 主图标 | `build/app.ico`（windres 链进 exe） | 白底圆角卡片，贴在资源管理器里 |

`internal/tray/icons/` 是 go:embed 的**唯一**目标目录（embed 只认包目录内的文件），
生成物由 `build/icons/build-icons.ps1` 直接写进去，所以它进仓库、构建时不需要复制。

## 关键不变量

1. **Headless 路径不构造托盘、不启管道监听。** 早先 C# 版在无界面模式下也
   `new TrayApp(...)`，导致短暂闪出托盘图标、甚至走错启动分支。
2. **`Stop()` 只停本端口那一个。** 见 [PITFALLS.md](PITFALLS.md#-stop-的判定必须按命令行---port不能猜)。
3. **打开网页走 `ShellExecuteW`，打开文件夹走 `explorer.exe`。**
4. **升级绝不在服务运行时改安装目录。** 运行中 `sharp.node`/`koffi.node`
   被独占锁定，`npm install` 会报 EBUSY 并把树改坏一半，连目录改名都会被拒。
   必须走「阶段 A 下载 → 阶段 B 停服换目录」，见 [UPDATE.md](UPDATE.md)。

## 为什么零第三方依赖

本机 `proxy.golang.org` DNS 不可达，`go get` 会失败。所以所有 Win32 绑定
都手写在 `internal/win32/` 里，没有 `golang.org/x/sys/windows`，也没有任何
托盘库。加依赖前先确认网络能通。
