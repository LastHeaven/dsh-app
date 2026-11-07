# AGENTS.md

给在这个仓库里工作的 AI / 人类的说明。

**这个文件只放"必须知道什么、去哪儿看"。** 技术细节都在 `docs/` 里，
改代码前按下面的索引去查对应章节。

## 项目是什么

`dsh web` 的 Windows 托盘外壳。常驻通知区显示服务状态，左键打开管理界面，
右键启停。职责就三件事：探测本机 `dsh web` 进程、按需启停、托盘显示状态。

技术上是 **Go + 纯 Win32 P/Invoke，零第三方依赖**，单文件约 3.0 MB。
演进史：脚本 → C#（自包含单文件 63.77 MB）→ Go 重写（体积降 95%）。

## 五条红线

改代码时这几条最容易踩，踩了会造成真实损坏：

1. **`-Stop` 只停命令行里 `--port` 匹配的那一个进程。**
   绝不能"杀掉所有匹配到的"，也不能"端口没归属就停唯一那个"——
   这两种写法都曾把用户正在用的 3080 杀掉。
   → [PITFALLS 进程与端口](docs/PITFALLS.md#-stop-的判定必须按命令行---port不能猜)

2. **不要拿 3080 做实验。** 那是用户正在用的实例。
   用 `-Port 3199` 隔离测试，且每次操作前后比对 3080 的 PID。
   → [TESTING 测试纪律](docs/TESTING.md#测试纪律)

3. **Headless 路径（`-DryRun`/`-Stop`/`-Headless`）绝不构造托盘、不启管道监听。**
   那是跑完即退的。

4. **Win32 窗口有线程亲和性。** `wndProc` 里不能做耗时操作；
   goroutine 里不能直接 `CreateWindow`/`DestroyWindow`。
   → [ARCHITECTURE 线程模型](docs/ARCHITECTURE.md#线程模型)

5. **打开网页用 `ShellExecuteW`，打开文件夹用 `explorer.exe`。**
   混用会导致"启动服务后弹出文档文件夹"。
   → [PITFALLS 窗口/托盘/Shell](docs/PITFALLS.md#打开网页必须用-shellexecutew)

## 文档索引

| 文档 | 讲什么 | 什么时候看 |
| --- | --- | --- |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 数据流、文件职责、线程模型、单实例、三态判定 | 接手项目、加功能、改结构 |
| [docs/PITFALLS.md](docs/PITFALLS.md) | 14 个真实踩过的坑，按主题分组 | **改相关代码前必读** |
| [docs/TESTING.md](docs/TESTING.md) | 构建、测试纪律、发布、验证清单 | 改完代码要验证时 |
| [README.md](README.md) | 面向用户：怎么用、命令行参数 | 想知道这程序干嘛的 |

## 速查

**构建**（本机没有 `pwsh`，只有 PowerShell 5.1）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

产物 `publish\dsh-web.exe`。依赖 Go 1.24+ 和 `windres`（msys64）。

**部署**：复制到 `D:\agents\dsh\dsh-web.exe`。该目录只有 exe，
运行期自动生成 `logs\` 等文件。

**代码结构**（详见 ARCHITECTURE）：

```
main.go      入口分流 + runHeadless
win32.go     全部 Win32 绑定
paths.go     路径/进程快照/命令行读取
service.go   Stop/Start/LastUrl/端口归属
tray.go      托盘窗口与消息循环
single.go    互斥体 + 命名管道
console.go   控制台 shim + 参数解析
icons.go     内嵌 ICO → LoadImageW
```

**几个不能改的约束**：

- 零第三方依赖 —— 本机 `go get` 网络不通，`proxy.golang.org` DNS 不可达
- manifest 必须 `requireAdministrator`，资源 id 必须是 1
- 不能有主窗口，只有托盘（用户明确说过"不需要 UI"）
- `.ps1` 必须存成 **UTF-8 带 BOM**（否则 PowerShell 5.1 按 GBK 解析报错）
