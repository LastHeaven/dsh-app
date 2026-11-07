# AGENTS.md

给在这个仓库里工作的 AI / 人类的说明。**先读「踩过的坑」一节再改代码**，
那里的每一条都是真实踩出来的，有些反直觉。

## 项目是什么

`dsh web` 的 Windows 托盘外壳。最早是两个脚本（`dsh-web-restart.cmd` +
`dsh-web-restart.ps1`），中间有过一版 C#（WPF + WinForms，单文件自包含 63.77 MB），
**现在是 Go 写的原生 Win32 程序，2.99 MB**。

职责就三件：探测本机 `dsh web` 进程、按需启停、托盘显示状态。

## 技术选型与约束

- **Go + 纯 Win32 P/Invoke**，零第三方依赖。托盘用 `Shell_NotifyIcon`，
  消息循环自己写，**不引 WinForms/WPF/.NET**。
- **不引第三方 Go 模块**：本机 `proxy.golang.org` DNS 不可达，`go get` 会失败。
  所有 Win32 绑定都在 `win32.go` 里手写。加依赖前先确认网络。
- **windres 生成 `app.syso`**：把 `app.manifest` 和 `app.ico` 链进 exe。
  windres 在 `C:\msys64\ucrt64\bin\windres.exe`，不在 PATH 里，`build.ps1` 会去找。
- **manifest 要求管理员权限**（`requireAdministrator`）：要结束旧进程、后台拉起
  node，非管理员做不到。资源 id **必须是 1**，用别的 id 系统不把它当清单读，
  提权就不生效了。
- **不能有主窗口**：只有托盘。用户明确说过「不需要 UI」。

## 踩过的坑（务必保留这些防护）

### 1. `-Stop` 绝不能杀「所有」dsh web 进程，也不能「停掉唯一那个」

**这条出过两次真实事故，两次都是 3080 被误杀。**

- 第一次（C# 早期）：照搬 `.ps1` 的语义（重启时杀掉全部实例），用 `-Port 3199`
  隔离测试时把用户正在用的 3080 一起杀了。
- 第二次（Go 重写时）：我把 C# 版的回落规则原样抄了过来 ——
  「端口已无归属且全机只有 1 个 web 实例时停它」。结果 `-Stop -Port 3199`
  （3199 已空）时，那「唯一一个」正是 3080，于是又被杀了。
  **回落规则本身就是错误方向，已彻底删除。**

现在 `Stop(port)` 的判定依据是**进程命令行里的 `--port`**，不是猜：

1. 只停命令行里明确写了 `--port <port>` 的进程；
2. **没写 `--port` 的默认就是 3080**，只有目标端口是 3080 时才可能成为候选；
3. 端口归属 PID 与候选交叉验证，不一致就不动；
4. **任何情况下都不存在「停掉唯一一个 web」这种回落**；
5. 不递归杀子进程树。

原则：**宁可「该停的没停」，也绝不「多停一个」。**
`procListensOnPort()` 是这条规则的实现，改它之前先读完这段。

### 2. 不要递归杀子进程树

早期用父子关系递归 `KillTree`，结果把本进程也带走了（PID 复用导致父子关系失真），
表现为「命令看似成功、零输出、目标没死」。现在只对目标 PID 直接终止，不递归。

### 3. 命令行必须用 NtQueryInformationProcess class 60，不要用 WMI

- **WMI(`System.Management`) 在原生/裁剪下运行时会崩**：编译能过，一跑就
  `MissingMethodException: 'System.Management.WbemDefPath'`（依赖反射）。
  这是当年 C# AOT 路线的死因。
- **读 PEB 那条路走不通**：`NtQueryInformationProcess` + `ReadProcessMemory` 读
  `node.exe` 的 PEB 返回 `ERROR_ACCESS_DENIED(5)`，已放弃，别重试。
- **可用的是 class 60**（`ProcessCommandLineInformation`）：只要
  `PROCESS_QUERY_LIMITED_INFORMATION` 权限，实测能读全本机所有 node 进程的命令行。

读回来的结构是 `PROCESS_COMMANDLINE_INFORMATION { UNICODE_STRING }`，x64 布局：
`Length(0,u16) MaxLength(2,u16) pad(4) Buffer(8,ptr)`。
**Buffer 指向我们自己的 buf 内部**，要从 buf 按偏移读；把 `uintptr` 转回指针做加法
会被 `go vet` 判为 `possible misuse of unsafe.Pointer`（构建脚本会拦下）。

### 4. `GetExtendedTcpTable` 的结构和字节序

`MIB_TCPROW_OWNER_PID` 每行 24 字节，表头 4 字节计数，行内布局：

```
dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
dwRemotePort(16) dwOwningPid(20)
```

**端口号是高字节序存在低 16 位**，必须先换字节序：
`((raw & 0xFF) << 8) | ((raw >> 8) & 0xFF)`。

表类型用 `TCP_TABLE_OWNER_PID_ALL = 5`，不是 `..._LISTENER = 3`。
LISTEN 状态值是 2。

### 5. `CreateIconFromResourceEx` 在这台机器上不可用

从内存里的 ICO 图块直接建 HICON：**所有帧、所有参数组合都返回 0，
而且不设置 last-error**（`isIcon=1/0`、`16x16/0x0`、改 biHeight 都试过）。
别在这条路上浪费时间。

**可用做法**：把内嵌的 `.ico` 解到临时文件，用 `LoadImageW` 加载（C# 版也是这样）。
文件名带内容哈希，避免上次残留的半截文件让加载失败。
兜底：加载失败就用系统默认图标，**宁可图标不对，也不能没图标**。

三态图标颜色必须区分（`#0B0B0F` 运行中 / `#9AA0AE` 未运行 / `#D99B00` 启动中），
形状取 `icon-src/svg/whale.svg`，改了跑 `icon-src\build-icons.ps1` 重新生成。

### 6. `CreateMutexW` 的 ERROR_ALREADY_EXISTS 要取 Call 的返回值

`syscall` 里 `proc.Call(...)` 的**第三个返回值**就是本次调用的 last-error，
必须用它判断 183。别用 `syscall.GetLastError()` —— 那是进程级状态，
中间任何一次 syscall 都会覆盖它。另外**不要把** `syscall.Errno` 和无类型常量
直接比（类型不同会永远不相等），比较 `uintptr(en) == 183`。

### 7. 命名管道方向别搞反

服务端是**从客户端读命令**，所以服务端建 `PIPE_ACCESS_INBOUND(1)`，客户端用
`GENERIC_WRITE`。最初抄成 `PIPE_ACCESS_OUTBOUND(2)`，服务端拿不到读权限，
转发静默失败 —— 表现为「第二个实例既不退出、也不提示、还能开出第二个托盘」。

### 8. `NotifyIcon.Text` 上限 63

超了 `Shell_NotifyIcon` 会失败。`setText()` 里有截断（`truncateBytes`，按字符切，
别把汉字切成半个）。

### 9. `last-url.txt` 里的地址必须校验端口

这个文件是"最近一次启动写下的地址"，但它是**历史遗留**的：调试时用 `-Port 3199`
起过实例，就会把 3199 的地址写进去。之后用默认 3080 启动，如果把文件内容直接当
地址打开，就会跳到早已失效的 3199。

`LastUrl(port)` 必须用端口与目标端口比对，不符就忽略、改从日志里找该端口的地址。
**新增任何"打开地址"的逻辑都要传端口。**

### 10. 控制台中文输出用 WriteConsoleW

Go 在 Windows 上默认按 UTF-8 写 stdout，而 cmd/PowerShell 控制台是 GBK(936)，
直接写会乱码。规则：

- 输出到**真实控制台**：用 `WriteConsoleW` 传 UTF-16；
- 输出被**重定向到管道/文件**：直接 UTF-8 字节流。

`windowsgui` 子系统的进程默认没有控制台，要先 `AttachConsole(ATTACH_PARENT_PROCESS)`。
注意「有没有控制台」和「stdout 是不是管道」是两件独立的事，要分别判断。

### 11. `.ps1` 必须存成 UTF-8 **带 BOM**

Windows PowerShell 5.1 按 GBK 解析无 BOM 的 `.ps1`，中文会乱码并导致引号配对
错乱、语法报错。`build.ps1` 和 `icon-src/build-icons.ps1` 都是带 BOM 的。
改完 `.ps1` 要确认前三个字节是 `EF BB BF`。

### 12. 也尽量别用 PowerShell 的 `Set-Content` 改 `.go` 文件

它会把文件写成带 BOM 或 GBK，Go 编译器直接报 `illegal UTF-8 encoding`。
改代码用编辑器/写入工具，不要用 shell 重定向。

### 13. 打开网页必须用 ShellExecuteW，不能用 exec.Command("explorer.exe", url)

**症状**：托盘启动服务后，会莫名其妙弹出一个「文档 - 文件资源管理器」窗口。

**根因**：`exec.Command("explorer.exe", url)` 不是打开网页的正确姿势。
Go 的 exec.Command 会按 MS 转义规则把参数拼成命令行，而 `explorer.exe`
对引号的处理非常特殊：它把带引号的 URL 当成"找不到的路径"，
于是退化成的默认行为 —— 打开「文档」文件夹。

**实测对比**（同一个带 token 的真实 URL）：

| 方式 | 结果 |
| --- | --- |
| `exec.Command("explorer.exe", url)` | 打开「文档」文件夹 |
| `exec.Command("explorer.exe", url)` + `HideWindow:true` | 没开文件夹（碰巧，不可靠） |
| `ShellExecuteW("open", url, ...)` | 正常调起浏览器 |

**正确做法**：`shellExecuteOpen()`（`win32.go`），它就是 C# 版
`UseShellExecute = true` 背后的东西。返回值 `> 32` 才算成功。
**顺带的好处**：浏览器由 shell 以用户身份启动，不会被本程序的管理员权限
带成提权进程。

`OpenUrl` 还加了 `http/https` 前缀白名单，防止把奇怪字符串交给 shell。

`OpenFolder`（打开安装目录/日志）**仍然用 explorer.exe** —— 那里是真的要开
文件夹，语义正确，别一起改掉。

## 代码结构

| 文件 | 职责 |
| --- | --- |
| `main.go` | 入口：分流转托盘 / `runHeadless`，参数解析输出、`diagLog` |
| `win32.go` | 所有 Win32 API 绑定、常量、结构体 |
| `paths.go` | 路径定位、node/bin.js 查找、进程快照、`commandLineOf`(class 60) |
| `service.go` | `Stop(port)`、`Start()`、`LastUrl(port)`、TCP 端口归属、`procListensOnPort` |
| `tray.go` | 托盘窗口、`Shell_NotifyIcon`、右键菜单、三态轮询 |
| `single.go` | 互斥体 + 命名管道单实例转发 |
| `console.go` | 控制台 shim（`WriteConsoleW`）、`Options` 解析、帮助 |
| `icons.go` | 内嵌 ICO → 临时文件 → `LoadImageW` |
| `build.ps1` | windres 生成 syso → gofmt → go vet → go build |

**关键不变量**：`Headless` 路径（`-DryRun`/`-Stop`/`-Headless`）绝不构造托盘，
也绝不启动管道监听 —— 那是跑完即退的。早先 C# 版在无界面模式下也 `new TrayApp(...)`，
导致短暂闪出托盘图标、甚至走错启动分支。

## 构建与验证

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

产物 `publish\dsh-web.exe`，约 **3.0 MB**。脚本会：
gofmt → windres 生成 `app.syso` → `go vet`（必须 0 告警）→ `go build`（`-s -w -H windowsgui`）
→ 校验 manifest 是否嵌进去了。

注意本机**没有 `pwsh`**（只有 PowerShell 5.1），用 `powershell` 调脚本。

**测试纪律（血泪）**：

- **不要拿 3080 做实验**，那是用户正在用的实例。用 `-Port 3199` 之类的空闲端口。
- 手工测试前先记录 3080 的 PID，操作后比对，并显式打印 PASS/FAIL ——
  这条纪律实际拦下过第三次误杀。
- 测完 `-Stop -Port 3199` 收尾，并确认 3080 没变。
- 验证「停止」时**只测不会命中目标的场景**：想确认 `-Stop -Port 3080` 正确，
  就别真跑它，去看 `-DryRun` 和 `procListensOnPort` 的判定结果。

## 部署

发布产物放 `D:\agents\dsh\dsh-web.exe`。

**部署目录现在只有 exe**（外加运行期自动生成的 `logs\` 和 `headless.log`）。
早期的 `dsh-web-restart.cmd` / `dsh-web-restart.ps1` / `dsh-web-stop.cmd`
和 `dsh web.lnk` 快捷方式**已被用户明确要求删除**，不要再往那儿加备用入口。
`icons\` 也已删除（原先只是给快捷方式提供图标）。

`Paths.Root()` 因此不再靠"旁边有没有某个标记文件"来定位安装目录，
改为**以 exe 所在目录为准**，只在目录不可写时退到 `D:\agents\dsh`。
别再把这个判定改回依赖具体文件名 —— 那些文件都已经不存在了。

## 图标

`assets\*.ico` 是生成物，改了鲸鱼形状或配色后跑：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File icon-src\build-icons.ps1
```

鲸鱼路径在 `icon-src\svg\whale.svg`。
