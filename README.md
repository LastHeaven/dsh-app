# dsh-app

`dsh web` 的 Windows 桌面外壳：常驻托盘显示服务状态，双击即可拉起管理界面。

原本是一批零散的脚本（`dsh-web-restart.cmd` → `dsh-web-restart.ps1`），
现在封装成一个自包含的单文件 exe，不再依赖 PowerShell 转发，也不再弹黑窗口。

## 行为

**托盘图标**（右下角通知区，透明底鲸鱼 —— 形状取自 dsh 的 `icon.svg`）

| 图标 | 含义 |
| --- | --- |
| 黑色鲸鱼 | 运行中 |
| 琥珀色鲸鱼 | 启动中 / 启动失败 |
| 灰色鲸鱼 | 未运行 |

左键点击 = 打开管理界面。右键菜单：打开管理界面 / 启动 / 停止 / 重启 /
打开访问地址 / 打开安装目录 / 查看日志 / 退出。

**命令行**（跑完即退，不显示托盘，供脚本和计划任务调用）

```
dsh-web.exe                 启动并显示托盘
dsh-web.exe -Port 8080      指定端口，停止时也只影响该端口
dsh-web.exe -Stop           只关闭该端口上的实例
dsh-web.exe -DryRun         只检测并输出
dsh-web.exe -NoBrowser      不自动打开浏览器
dsh-web.exe -KeepExisting   不关闭已有实例
dsh-web.exe -Headless       不显示托盘，按命令行跑完就退出
dsh-web.exe -Help           帮助
```

## 构建

需要 .NET 8 SDK。

```powershell
# 单文件、自包含（目标机器无需安装 .NET 运行时）
dotnet publish -c Release -r win-x64 --self-contained true `
  -p:PublishSingleFile=true -p:IncludeNativeLibrariesForSelfExtract=true `
  -p:EnableCompressionInSingleFile=true -o publish
```

产物 `publish\dsh-web.exe` 约 64 MB。

## 图标

`assets\*.ico` 和 `app.ico` 是生成物，由 `icon-src\build-icons.ps1` 产出：

```powershell
pwsh -File icon-src\build-icons.ps1
```

依赖 Chrome（渲染 SVG）和 Python + Pillow（打包 ICO）。
鲸鱼路径来自 `icon-src\svg\whale.svg`（即原 `icon.svg` 的 `d` 属性）。

## 目录

| 路径 | 说明 |
| --- | --- |
| `Program.cs` | 全部逻辑：托盘、进程探测、启停、单实例 |
| `dshweb.csproj` | 项目定义与版本信息 |
| `app.manifest` | 要求管理员权限、长路径支持 |
| `app.ico` | exe 自身图标 |
| `assets\` | 托盘三态图标 |
| `icon-src\` | 图标生成脚本与 SVG 源 |

## 部署位置

目前发布到 `D:\agents\dsh\`，与 `dsh-web-restart.cmd` / `.ps1` 并存。
那两个脚本作为 exe 不可用时的备用入口**保留未动**。

## 注意

- 程序需要管理员权限（清单里 `requireAdministrator`），因为要结束旧进程、
  在后台拉起 node。双击会弹 UAC，属正常行为。
- 浏览器由 `explorer.exe` 代开，以保持普通用户权限 —— 提权进程直接
  `Start-Process` 会让浏览器也变成管理员身份。
- 日志在 exe 同级的 `logs\` 下，最近的带 token 地址写在 `last-url.txt`。
