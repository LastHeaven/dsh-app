# 测试与发布

## 构建

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1
```

产物 `publish\dsh-web.exe`，约 **3.0 MB**。

脚本流程：gofmt → windres 生成 `app.syso` → `go vet`（必须 0 告警）→
`go build -ldflags "-s -w -H windowsgui"` → 校验 manifest 是否嵌入。

依赖：

- Go 1.24+
- `windres`（`C:\msys64\ucrt64\bin\windres.exe`，不在 PATH，脚本会自动找）

`-s -w` 别去掉（体积会翻几倍），`-H windowsgui` 也别去掉（否则每次启动弹控制台）。

## 测试纪律

**这一节是血泪换来的，尤其第一条。**

### 不要拿 3080 做实验

3080 是用户正在用的实例。用 `-Port 3199` 之类的空闲端口做隔离测试。

### 每次操作前后比对 3080 的 PID

```powershell
$base = (Get-NetTCPConnection -LocalPort 3080 -State Listen).OwningProcess
# ... 操作 ...
$now = (Get-NetTCPConnection -LocalPort 3080 -State Listen).OwningProcess
"3080: $now (baseline $base) -> " + $(if($now -eq $base){"PASS"}else{"FAIL"})
```

显式打印 PASS/FAIL，别靠眼睛看。这条纪律实际拦下过第三次误杀。

### 测完收尾

```powershell
dsh-web.exe -Stop -Port 3199
```

并再次确认 3080 没变。

### 验证「停止」时只测不会命中目标的场景

想确认 `-Stop -Port 3080` 正确，**别真跑它**。去看 `-DryRun` 的输出和
`procListensOnPort()` 的判定结果。这两个都不产生副作用。

### 验证托盘 GUI 不必靠截图

托盘常驻但窗口不可见，截图经常截到别的窗口。用这些更可靠：

- 找窗口：`FindWindowW("DshWebTrayWnd", null)`，非 0 说明窗口建出来了
- 查状态：`diagLog()` 写的 `D:\agents\dsh\tray-diag.log`
- 触发命令：`PostMessage(hwnd, WM_COMMAND, cmdExit, 0)` 等价于点菜单

枚举资源管理器窗口（判断有没有误开文件夹）：

```powershell
EnumWindows + GetClassNameW == "CabinetWClass"
```

## 发布到 D:\agents\dsh

```powershell
Copy-Item .\publish\dsh-web.exe D:\agents\dsh\dsh-web.exe -Force
```

替换前先确认没有托盘在跑（`Get-Process dsh-web`），否则文件被占用。

部署目录**只有 exe**，另外运行期自动生成 `logs\`、`headless.log`、
`last-url.txt`、`tray-diag.log`。

## 更新功能怎么验

更新会动全局安装目录，**风险最高的模块**。分三层验：

### 1. 单元测试（每次改都跑，秒级）

```powershell
go test ./...
```

覆盖 SemVer 比较（重点是 `rc.10 > rc.9` 这种数值段）、`isNewer` 的
"垃圾输入一律不报更新"、JSON 顶层键抽取（不能取到 `dependencies` 里的同名键）、
URL 拆分。

### 2. 只读验证（不碰任何东西）

```powershell
dsh-web.exe -CheckUpdate
```

会打印安装目录、当前版本、最新版本。**这个命令完全不写入**，
随时可以跑。改完检测逻辑先跑它。

### 3. 完整升级（必须用隔离端口）

```powershell
# 记基线
$base = (Get-NetTCPConnection -LocalPort 3080 -State Listen).OwningProcess

# 起一个 3199 实例当"受害者"
dsh-web.exe -Port 3199 -NoBrowser -Headless

# 强制重装同版本 —— 等价于完整跑一遍阶段 A + 阶段 B
dsh-web.exe -UpdateTo <当前版本> -Force -Port 3199

# 比对 3080
$now = (Get-NetTCPConnection -LocalPort 3080 -State Listen).OwningProcess
"3080: $now (baseline $base) -> " + $(if($now -eq $base){"PASS"}else{"FAIL"})
```

`-Force` + 同版本是**刻意设计**的测试手段：它完整走一遍下载 → 换目录 →
启动 → 校验 → 回滚路径，但版本号不变，风险最小。

失败路径也要验：`-UpdateTo 9.9.9` 这种不存在的版本，必须
「报 ETARGET + 删干净 stage + 服务不动」。验完检查：

```powershell
Test-Path D:\npm\.dsh-web-stage            # 必须是 False
(Get-Content ...\dsh\package.json | ConvertFrom-Json).version   # 必须没变
```

改升级逻辑前先读 [UPDATE.md](UPDATE.md)。

## 图标重新生成

改了鲸鱼形状或配色后：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File icon-src\build-icons.ps1
```

需要 Chrome（渲染 SVG）和 Python + Pillow（打包 ICO）。之后要重新跑
`build.ps1`，windres 才会把新的 `app.ico` 链进 exe。

## 验证清单

改完发版前过一遍：

- [ ] `go vet` 0 告警
- [ ] `go test ./...` 全过
- [ ] `-DryRun` 能识别到运行中的实例
- [ ] `-Help` 中文不乱码
- [ ] `-CheckUpdate` 能正确报告版本
- [ ] 3199 起服务 → 托盘变黑鲸鱼 → 退出 → 3199 进程和端口都消失
- [ ] 退出后用 3199 重启，不报端口占用
- [ ] 全程 3080 的 PID 未变
