# 踩过的坑

每一条都是真实踩出来的，有些反直觉。**改相关代码前先读对应那条。**
按主题分组，不按发现顺序。

---

## 进程与端口

### `-Stop` 的判定必须按命令行 `--port`，不能猜

**出过两次真实事故，两次都是用户正在用的 3080 被误杀。**

- 第一次（C# 早期）：照搬 `.ps1` 的语义「重启时杀掉全部实例」，用 `-Port 3199`
  隔离测试时把 3080 一起杀了。
- 第二次（Go 重写时）：把 C# 版的回落规则原样抄了过来 ——
  「端口已无归属且全机只剩 1 个 web 实例时停它」。结果 `-Stop -Port 3199`
  （3199 已空）时，那「唯一一个」正是 3080，于是又被杀了。
  **回落规则本身就是错误方向，已彻底删除。**

现在的规则：

1. 只停命令行里明确写了 `--port <port>` 的进程；
2. **没写 `--port` 的就是默认 3080**，只有目标端口是 3080 时才可能成为候选；
3. 端口归属 PID 与候选交叉验证，不一致就不动；
4. **任何情况下都不存在「停掉唯一一个 web」这种回落**；
5. 不递归杀子进程树。

原则：**宁可「该停的没停」，也绝不「多停一个」。**

实现在 `procListensOnPort()`（`service.go`）。改它之前先读完本节。

### 不要递归杀子进程树

早期用父子关系递归 `KillTree`，结果把本进程也带走了（PID 复用导致父子关系
失真），表现为「命令看似成功、零输出、目标没死」。现在只对目标 PID 直接
终止，不递归。

### `GetExtendedTcpTable` 的结构和字节序

`MIB_TCPROW_OWNER_PID` 每行 24 字节，表头 4 字节计数，行内布局：

```
dwState(0) dwLocalAddr(4) dwLocalPort(8) dwRemoteAddr(12)
dwRemotePort(16) dwOwningPid(20)
```

**端口号是高字节序存在低 16 位**，必须先换字节序：
`((raw & 0xFF) << 8) | ((raw >> 8) & 0xFF)`。

表类型用 `TCP_TABLE_OWNER_PID_ALL = 5`，不是 `..._LISTENER = 3`。
LISTEN 状态值是 2。

---

## 读取进程命令行

### 必须用 NtQueryInformationProcess class 60

- **WMI(`System.Management`) 在原生/裁剪下运行时会崩**：编译能过，一跑就
  `MissingMethodException: 'System.Management.WbemDefPath'`（依赖反射）。
  这是当年 C# AOT 路线的死因。
- **读 PEB 那条路走不通**：`NtQueryInformationProcess` + `ReadProcessMemory` 读
  `node.exe` 的 PEB 返回 `ERROR_ACCESS_DENIED(5)`，已放弃，别重试。
- **可用的是 class 60**（`ProcessCommandLineInformation`）：只要
  `PROCESS_QUERY_LIMITED_INFORMATION` 权限，实测能读全本机所有 node 进程的命令行。

读回来的结构是 `PROCESS_COMMANDLINE_INFORMATION { UNICODE_STRING }`，x64 布局：
`Length(0,u16) MaxLength(2,u16) pad(4) Buffer(8,ptr)`。

**Buffer 指向我们自己的 buf 内部**，要从 buf 按偏移读。把 `uintptr` 转回指针
做加法会被 `go vet` 判为 `possible misuse of unsafe.Pointer`（`build.ps1` 会拦下）。

---

## 窗口 / 托盘 / Shell

### 打开网页必须用 ShellExecuteW

**症状**：托盘启动服务后，莫名其妙弹出「文档 - 文件资源管理器」。

**根因**：`exec.Command("explorer.exe", url)` 不是打开网页的正确姿势。
Go 会按 MS 转义规则把参数拼成命令行，而 `explorer.exe` 对引号的处理很特殊：
它把带引号的 URL 当成"找不到的路径"，退化成打开默认的「文档」文件夹。

实测对比（同一个带 token 的真实 URL）：

| 方式 | 结果 |
| --- | --- |
| `exec.Command("explorer.exe", url)` | 打开「文档」文件夹 |
| 同上 + `HideWindow:true` | 没开文件夹（碰巧，不可靠） |
| `ShellExecuteW("open", url, ...)` | 正常调起浏览器 |

**正确做法**：`shellExecuteOpen()`（`win32.go`），它就是 C# 版
`UseShellExecute = true` 背后的东西。返回值 `> 32` 才算成功。
**顺带的好处**：浏览器由 shell 以用户身份启动，不会被本程序的管理员权限
带成提权进程。

`OpenUrl` 另加了 `http/https` 前缀白名单。

`OpenFolder`（打开安装目录/日志）**仍然用 explorer.exe** —— 那里是真的要开
文件夹，语义正确，别一起改掉。

### 退出托盘要顺手停掉它管的服务

**症状**：点「退出」，图标消失，但 dsh web 还在后台跑。

**规则**：`exit()` 必须先 `Stop(t.opt.Port)` 再收尾。停的仍然是本端口那一个
（见上面第 1 条），不要退化成"停所有"。

**两个实现细节，改动时别丢**：

1. `exit()` 里 `Stop()` 最多等 20 秒，必须由 **goroutine** 调用
   （`dispatchCommand` 里 `go t.exit()`）。同步调会卡死消息循环。
2. `DestroyWindow` 只能在创建窗口的线程上调，而 `exit()` 跑在 goroutine 里。
   所以它最后是 `PostMessage(hwnd, WM_CLOSE)` 绕回 `wndProc`，由 `wmClose`
   分支在本线程 `destroyWindow`。**不要在 goroutine 里直接 destroyWindow。**

顺序上先 `Shell_NotifyIcon(NIM_DELETE)` 收图标，再 `Stop()` —— 让用户立刻
看到"退出了"，不必等进程终结。

### `NotifyIcon.Text` 上限 63

超了 `Shell_NotifyIcon` 会失败。`setText()` 里有截断（`truncateBytes`，按字符切，
别把汉字切成半个）。

### `CreateMutexW` 的 ERROR_ALREADY_EXISTS 要取 Call 的返回值

`syscall` 里 `proc.Call(...)` 的**第三个返回值**就是本次调用的 last-error，
必须用它判断 183。别用 `syscall.GetLastError()` —— 那是进程级状态，
中间任何一次 syscall 都会覆盖它。

另外**不要**把 `syscall.Errno` 和无类型常量直接比（类型不同会永远不相等），
比较 `uintptr(en) == 183`。

### 命名管道方向别搞反

服务端是**从客户端读命令**，所以服务端建 `PIPE_ACCESS_INBOUND(1)`，客户端用
`GENERIC_WRITE`。最初抄成 `PIPE_ACCESS_OUTBOUND(2)`，服务端拿不到读权限，
转发静默失败 —— 表现为「第二个实例既不退出、也不提示、还能开出第二个托盘」。

---

## 图标

### `CreateIconFromResourceEx` 在这台机器上不可用

从内存里的 ICO 图块直接建 HICON：**所有帧、所有参数组合都返回 0，
而且不设置 last-error**（`isIcon=1/0`、`16x16/0x0`、改 biHeight 都试过）。
别在这条路上浪费时间。

**可用做法**：把内嵌的 `.ico` 解到临时文件，用 `LoadImageW` 加载。
文件名带内容哈希，避免上次残留的半截文件让加载失败。
兜底：加载失败就用系统默认图标，**宁可图标不对，也不能没图标**。

### 两套图标不能混用

- **托盘三态**（`assets/*.ico`）：**透明底**，叠在任务栏上。
- **exe 主图标**（`app.ico`）：**白底圆角卡片**，叠在资源管理器/开始菜单的
  深浅背景上。

三态颜色必须区分：`#0B0B0F` 运行中 / `#9AA0AE` 未运行 / `#D99B00` 启动中。
形状取 `icon-src/svg/whale.svg`。

---

## 更新机制

### 服务运行中改安装目录必定失败

**不是"可能失败"，是必定失败。** 实测：服务跑着的时候 `sharp.node` 和
`koffi.node` 处于独占锁定状态，此时

```
npm install（换版本） → EBUSY -4082，rename 失败
Rename-Item 整目录    → Access to the path is denied
Directory.Move        → Access to the path is denied
```

更麻烦的是 npm 失败后**树被改坏一半**：`package.json` 要求 `node-pty@1.0.0`，
实际装的是 `1.1.0`，`npm ls` 报 `ELSPROBLEMS`。新版没生效、旧版也不完整。

所以升级必须走「先停服务，再换目录」，见 [UPDATE.md](UPDATE.md)。

### 别用 `net/http` 做更新检查

实测二进制体积：

| 做法 | 体积 |
| --- | --- |
| 无网络能力 | 3,140,608 B |
| WinHTTP P/Invoke | 3,212,288 B（+68 KB） |
| `net/http` + `encoding/json` | 5.62 MB（+2.6 MB） |

`net/http` 会拖进 http2、DNS resolver、reflect。为了查一个 67 字节的
dist-tags 多付 2.6 MB，等于把 Go 重写省下的体积还回去一半。

同理 `encoding/json` 单独就要 +264 KB（实测），所以 JSON 是手写的。
**手写解析只认深度 1 的键** —— package.json 的 `dependencies` 里也有
`@deepseek-ai/dsh` 这种同名键，不限制深度会把依赖的版本当成包自己的版本。

### WinHTTP 的坑

1. **必须带 `WINHTTP_FLAG_SECURE(0x00800000)`**，否则它按明文 HTTP 发，
   服务器回 `400 The plain HTTP request was sent to HTTPS port`。
2. **`WinHttpQueryHeaders` 要加 `WINHTTP_QUERY_FLAG_NUMBER`**，不加拿到的
   不是 DWORD，状态码永远读成 0。
3. **一定要设超时**（`WinHttpSetTimeouts`）。没超时的 HTTPS 调用在托盘程序里
   等于线程泄漏，DNS 卡住时 goroutine 会挂几个小时。
4. 默认走系统代理（`WINHTTP_ACCESS_TYPE_DEFAULT_PROXY`），这点比自己实现要好。

### 升级不能被"同版本"挡住

最初的正则是"目标必须比本地新"，结果连**修复损坏安装**都做不了。
现在拆成三种情况：

- 目标更新 → 正常升级
- 目标相同 → 拒绝，除非 `-Force`
- 目标更旧 → 拒绝，除非 `-Force`

`-Force` 存在的理由就是：服务运行时 npm 把树改坏之后，"重装同版本"
是唯一修复手段。

### 备份目录要唯一名字，删不掉也不能阻塞升级

**Windows 上文件被映射时，删除和目录改名都会失败** —— 目录重命名会检查
内部文件是否被打开。备份里恰好有 `libvips-42.dll` 这类 native 库，只要有过
node 进程映射它（哪怕进程已死、映射未释放），就会：

```
os.RemoveAll(backup) → Access is denied
os.Rename(backup, x) → Access is denied
```

用定长 `.dsh-web-old` 的话，**下一次升级会被永久堵死**：清理失败就中止，
而清理永远失败。

规则：

1. 备份名带纳秒时间戳（`.dsh-web-old-<UnixNano>`），每次都是新路径；
2. 开头扫历史备份尽力删，**删不掉就跳过，绝不阻塞**；
3. 结尾限时 12 秒删，超时留给下次。

同理，`go os.RemoveAll(...)` 做删除也是错的：737 MB 要 18 秒，
命令行模式跑完就 `os.Exit`，goroutine 被掐掉，备份原封不动留着。

### `npm install --prefix` 和 `-g --prefix` 布局不同

**这条实际踩到过，值得单独记。**

不带 `-g` 是"本地安装"语义，依赖**横向提升到 `<stage>\node_modules` 平铺**；
带上 `-g` 才是"全局安装"语义，依赖**嵌套在包自己的 `node_modules` 里**。

而真正装到 `D:\npm` 的是后者：扁平层 `@deepseek-ai` 只有 dsh 一个，
329 个依赖全在 `dsh\node_modules\@deepseek-ai` 下面。

拿平铺的树去换目录，dsh 启动立刻报：

```
ERR_MODULE_NOT_FOUND: Cannot find package '@deepseek-ai/dsh-app-boot'
imported from D:\npm\node_modules\@deepseek-ai\dsh\lib\bin.js
```

**回滚在这里救了一次**：新版本起不来 → 自动换回旧版 → 重新拉起，
用户只看到"更新失败"，服务没断过。

### 备份目录不能放进 `@deepseek-ai` 作用域

`installRoot + ".old"` 看着自然，实际会变成
`node_modules\@deepseek-ai\dsh.old` —— 而 npm 扫 `node_modules` 时
**不校验目录名合法性，只要是目录就当已安装的包**：

```
+-- @deepseek-ai/dsh.broken@npm:@deepseek-ai/dsh@0.2.2-rc.1
+-- @deepseek-ai/dsh@0.2.2-rc.1
```

`npm ls -g` 里出现两个 dsh，后续操作都会被污染。
所以备份/损坏副本一律放 `<prefix>\.dsh-web-old`、`<prefix>\.dsh-web-broken`。

---

## 路径与文件

### `last-url.txt` 里的地址必须校验端口

这个文件是"最近一次启动写下的地址"，但它是**历史遗留**的：调试时用
`-Port 3199` 起过实例，就会把 3199 的地址写进去。之后用默认 3080 启动，
如果把文件内容直接当地址打开，就会跳到早已失效的 3199。

`LastUrl(port)` 必须用端口与目标端口比对，不符就忽略、改从日志里找该端口的
地址。**新增任何"打开地址"的逻辑都要传端口。**

---

## 控制台

### 中文输出用 WriteConsoleW

Go 在 Windows 上默认按 UTF-8 写 stdout，而 cmd/PowerShell 控制台是 GBK(936)，
直接写会乱码。规则：

- 输出到**真实控制台**：用 `WriteConsoleW` 传 UTF-16；
- 输出被**重定向到管道/文件**：直接 UTF-8 字节流。

`windowsgui` 子系统的进程默认没有控制台，要先 `AttachConsole(ATTACH_PARENT_PROCESS)`。
注意「有没有控制台」和「stdout 是不是管道」是两件独立的事，要分别判断。

---

## 构建工具链

### `.ps1` 必须存成 UTF-8 带 BOM

Windows PowerShell 5.1 按 GBK 解析无 BOM 的 `.ps1`，中文会乱码并导致引号配对
错乱、语法报错。`build.ps1` 和 `icon-src/build-icons.ps1` 都是带 BOM 的。
改完 `.ps1` 要确认前三个字节是 `EF BB BF`。

### 别用 PowerShell 的 `Set-Content` 改 `.go` 文件

它会把文件写成带 BOM 或 GBK，Go 编译器直接报 `illegal UTF-8 encoding`。
改代码用编辑器/写入工具，不要用 shell 重定向。

### 本机没有 `pwsh`

只有 PowerShell 5.1。调脚本用 `powershell -NoProfile -ExecutionPolicy Bypass -File ...`。
