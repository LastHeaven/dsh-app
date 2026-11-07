# AGENTS.md

给在这个仓库里工作的 AI / 人类的说明。**先读「踩过的坑」一节再改代码**，
那里的每一条都是真实踩出来的，有些反直觉。

## 项目是什么

`dsh web` 的 Windows 托盘外壳。原先是 `dsh-web-restart.cmd` + `dsh-web-restart.ps1`
两个脚本，现在是一个自包含单文件 exe（WPF + WinForms，net8.0-windows）。

职责就三件：探测本机 `dsh web` 进程、按需启停、托盘显示状态。

## 技术选型与约束

- **WinExe + WPF**：需要 `NotifyIcon`（WinForms）和 `Application` 消息循环（WPF），
  两者都开（`UseWPF` + `UseWindowsForms`）。
- **单文件自包含发布**：目标机器没有 .NET 运行时，必须
  `--self-contained` + `PublishSingleFile`。
- **manifest 要求管理员权限**：要结束旧进程、后台拉起 node，非管理员做不到。
- **不能有主窗口**：只有托盘。用户明确说过「不需要 UI」。

## 踩过的坑（务必保留这些防护）

### 1. `-Stop` 绝不能杀「所有」dsh web 进程

最初照搬 `.ps1` 的语义（重启时杀掉全部实例），结果用 `-Port 3199` 做隔离测试时
把用户正在用的 3080 实例一起杀了。**这是真实事故。**

现在 `Stop(port)` 必须：
1. 用 `GetExtendedTcpTable` 查端口归属的 PID；
2. 确认该 PID 确实在 `LiveWeb()` 的匹配结果里；
3. 两条都满足才动手。

回落规则：端口已无归属（进程刚退出）且全机只有 1 个 web 实例时才停它。
**任何新逻辑都不要退化成"杀掉所有匹配到的进程"。**

### 2. 不要递归杀子进程树

早期用 WMI 的 `ParentProcessId` 递归 `KillTree`，结果把本进程也带走了
（PID 复用导致父子关系失真），表现为「命令看似成功、零输出、目标没死」。
现在只对目标 PID 直接 `Kill()`，不递归。

### 3. WMI 查询只能做一次

同一进程里反复 `new ManagementObjectSearcher` 不稳定，曾导致 `-Stop` 静默崩掉。
现在用 `Snapshot()` 一次性抓取，缓存在静态字段里，后续只查内存。
需要拿到最新状态时显式调用 `InvalidateSnapshot()`。

### 4. `GetExtendedTcpTable` 的结构和字节序

`MIB_TCPROW_OWNER_PID` 每行 24 字节，表头 4 字节计数，行内布局：

```
dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
dwRemotePort(16) dwOwningPid(20)
```

**端口号是高字节序存在低 16 位**，必须先换字节序：
`((raw & 0xFF) << 8) | ((raw >> 8) & 0xFF)`。

表类型用 `TCP_TABLE_OWNER_PID_ALL = 5`，不是 `..._LISTENER = 3`。

### 5. 已证明走不通的方案

- **PEB 读命令行**（`NtQueryInformationProcess` + `ReadProcessMemory`）：
  读 `node.exe` 的 PEB 会返回 `ERROR_ACCESS_DENIED(5)`，放弃了。
  别浪费时间重试。
- **Python 调 Chrome 截图**：`--headless` 返回 0 但不出文件。
  PowerShell 的 `Start-Process` 一直可靠 —— 图标脚本因此是 `.ps1`。
- **Pillow 的 `save(sizes=[...])` 写多尺寸 ICO**：只会写进第一档。
  必须手工拼 ICO（见 `icon-src/pack_ico.py`）。

### 6. `.ps1` 必须存成 UTF-8 **带 BOM**

Windows PowerShell 5.1 按 GBK 解析无 BOM 的 `.ps1`，中文会乱码并导致
引号配对错乱、语法报错。仓库里 `icon-src/build-icons.ps1` 就是带 BOM 的。

### 7. `NotifyIcon.Text` 上限 63 字符

超了直接抛异常。`SetText()` 里有截断，别绕过它。

### 8. `last-url.txt` 里的地址必须校验端口

这个文件是"最近一次启动写下的地址"，但它是**历史遗留**的：
调试时用 `-Port 3199` 起过实例，就会把 3199 的地址写进去。之后用默认 3080
启动，如果把文件内容直接当地址打开，就会跳到早已失效的 3199。

`LastUrl(port)` 因此必须用 `Uri.Port` 与目标端口比对，不符就忽略、
改从日志里找该端口的地址。**新增任何"打开地址"的逻辑都要传端口。**

## 代码结构

`Program.cs` 单个文件，四个静态类 + 一个实例类：

| 类型 | 职责 |
| --- | --- |
| `Program` | 入口：解析参数，分流到托盘或 `Headless` |
| `Options` | 参数解析、帮助文本 |
| `Paths` | 根目录定位、node/bin.js 查找、开浏览器/文件夹 |
| `DshService` | 进程快照、`LiveWeb()`、`Stop(port)`、`Start()`、端口归属 |
| `Headless` | `-DryRun` / `-Stop` / `-Headless`，跑完即退，不碰托盘 |
| `TrayApp` | 托盘图标、右键菜单、状态轮询 |
| `SingleInstance` | 互斥体 + 命名管道，重复启动会把命令转交给已有实例 |

**关键不变量**：`Headless` 路径绝不构造 `TrayApp`。早先版本在无界面模式下也
`new TrayApp(...)`，导致短暂闪出托盘图标、甚至走错启动分支。

## 构建与验证

```powershell
dotnet build -c Release -o out          # 快速编译检查，必须 0 警告
dotnet publish -c Release -r win-x64 --self-contained true `
  -p:PublishSingleFile=true -p:IncludeNativeLibrariesForSelfExtract=true `
  -p:EnableCompressionInSingleFile=true -o publish
```

**测试纪律**：不要拿 3080 做实验，那是用户正在用的实例。
用 `-Port 3199` 之类的空闲端口做隔离测试，测完再 `-Stop -Port 3199` 收尾。
验证"是否影响别的实例"时，先记录 3080 的 PID，操作后比对。

## 部署

发布产物放 `D:\agents\dsh\dsh-web.exe`。
同目录的 `dsh-web-restart.cmd` / `dsh-web-restart.ps1` 是**备用入口**，
用户明确要求保留，不要删。

## 图标

`assets\*.ico` 是生成物，改了鲸鱼形状或配色后跑：

```powershell
pwsh -File icon-src\build-icons.ps1
```

鲸鱼路径在 `icon-src\svg\whale.svg`。三态只改填充色，形状必须一致：
`#0B0B0F` 运行中 / `#9AA0AE` 未运行 / `#D99B00` 启动中。
