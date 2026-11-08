# dsh-app

`dsh web` 的 Windows 桌面外壳：常驻托盘显示服务状态，双击即可拉起管理界面。

最早是一批零散的脚本（`dsh-web-restart.cmd` → `dsh-web-restart.ps1`），
中间曾封装成 C# 单文件 exe（63.77 MB），**现在是 Go 写的原生 Win32 程序，3.07 MB**，
不依赖 .NET 运行时，也不依赖任何第三方 Go 模块。

当前版本 **1.0.0**，可在 [Releases](https://github.com/LastHeaven/dsh-app/releases) 下载 `dsh-web.exe`。

## 使用前需要

exe 本身是自包含的（纯静态编译，只依赖 Windows 自带的系统 DLL），
但它管理的是 dsh 服务，所以目标机器需要先装好：

| 依赖 | 要求 |
| --- | --- |
| Windows | 64 位，Win7 及以上 |
| **Node.js** | ≥ 24.9.0（dsh 的 `engines.node` 要求） |
| **dsh** | `npm i -g @deepseek-ai/dsh` |
| 权限 | 需要管理员（双击会弹 UAC，这是刻意设计） |

缺 Node.js 或 dsh 时会给出中文提示，不会闪退。
exe 放在哪个目录都行（日志写在它旁边），但**别放 `C:\Program Files\` 下面**，
那里写日志会被拒。

## 行为

**托盘图标**（右下角通知区，鲸鱼形状取自 dsh 的 `icon.svg`）：

| 图标 | 含义 |
| --- | --- |
| 黑色鲸鱼 | 运行中 |
| 琥珀色鲸鱼 | 启动中 / 启动失败 |
| 灰色鲸鱼 | 未运行 |

左键点击 = 打开管理界面。右键菜单：打开管理界面 / 启动 / 停止 / 重启 /
打开访问地址 / 更新到新版本 / 检查更新 / 打开安装目录 / 查看日志 / 退出。

**自动更新**：托盘每 6 小时在后台查一次 dsh 有没有新版本，发现后气泡提示，
右键即可一键升级。升级过程分两段 —— 先在后台下载（服务照常运行），
再短暂停机几秒换目录。失败会自动回滚到旧版。详见 [docs/UPDATE.md](docs/UPDATE.md)。

**命令行**（跑完即退，不显示托盘，供脚本和计划任务调用）：

```
dsh-web.exe                 启动并显示托盘
dsh-web.exe -Port 8080      指定端口，停止时也只影响该端口
dsh-web.exe -Stop           只关闭该端口上的实例
dsh-web.exe -DryRun         只检测并输出
dsh-web.exe -NoBrowser      不自动打开浏览器
dsh-web.exe -KeepExisting   不关闭已有实例
dsh-web.exe -Headless       不显示托盘，按命令行跑完就退出
dsh-web.exe -CheckUpdate    查一次新版本就退出
dsh-web.exe -Update         升级到最新版
dsh-web.exe -UpdateTo 1.2.3 升级到指定版本
dsh-web.exe -Force          配合 -UpdateTo，允许重装同版本（修复损坏的安装）
dsh-web.exe -NoUpdateCheck  托盘不做后台更新检查
dsh-web.exe -Help           帮助
```

`-Stop` 只影响**指定端口**上的那个实例。不带 `--port` 启动的实例默认是 3080，
所以 `-Stop -Port 3199` 永远不会碰到 3080。详见
[docs/PITFALLS.md](docs/PITFALLS.md#-stop-的判定必须按命令行---port不能猜)。

## 构建

需要 Go 1.24+，以及 `windres`（`C:\msys64\ucrt64\bin\windres.exe`，用来把
manifest 和图标链进 exe）。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

产物 `publish\dsh-web.exe`，约 3.1 MB，带版本信息（右键属性可见）。

发版走 GitHub Actions：打 tag 即自动构建、测试、发布 Release。

```powershell
git tag -a v1.0.1 -m "1.0.1"
git push origin v1.0.1
```

详见 [docs/TESTING.md](docs/TESTING.md#发版)。

脚本流程：gofmt → windres 生成 `cmd\dsh-web\app.syso` → `go vet` → `go test`
→ `go build -ldflags "-s -w -H windowsgui"`
→ 校验 manifest 是否嵌入（决定 UAC 提权是否生效）。

> 不要把 `-s -w` 去掉，也别关掉 `-H windowsgui`：前者体积会翻几倍，
> 后者会让每次启动弹一个控制台窗口。

## UAC

`build/app.manifest` 声明了 `requireAdministrator`：结束旧进程、后台拉起 node
都需要管理员权限，所以双击时会正常弹 UAC。这是刻意设计，不是 bug。

## 图标

托盘图标（`internal\tray\icons\*.ico`）和 exe 图标（`build\app.ico`）是生成物，由 `build\icons\build-icons.ps1` 产出：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File build\icons\build-icons.ps1
```

## 目录约定

部署目录（`D:\agents\dsh`）**只有 `dsh-web.exe` 一个文件**，没有配套脚本。
程序把日志和"最近一次访问地址"写在 exe 旁边：

- `logs\dsh-web.out.log` / `.err.log` — node 的 stdout / stderr
- `logs\update.log` — 更新检查与升级记录（UTF-8；用 PowerShell 看得加 `-Encoding UTF8`）
- `last-url.txt` — 最近一次启动拿到的带 token 地址
- `headless.log` — 命令行模式的关键输出（控制台不可用时的兜底）

这些都是运行期自动生成的，删掉会重建。程序按 **exe 所在目录**定位它们，
所以整个目录可以整体搬到别处。

## 许可

[MIT License](LICENSE) © 2026 LastHeaven

