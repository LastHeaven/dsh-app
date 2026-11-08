# 更新机制

托盘版可以检查 dsh 是否有新版本，并一键升级。这份文档讲清楚它怎么工作、
为什么这么设计、以及哪些地方不能乱改。

代码在 [internal/updater/](../internal/updater/)、[internal/win32/net.go](../internal/win32/net.go)。

## 为什么升级要这么麻烦

dsh 是 npm 全局包，自己没有 `update` 子命令，升级只能走 npm。而直接
`npm install -g` 会遇到三个硬问题，全都是实测出来的：

### 1. 运行中升级会把安装树改坏

服务跑着的时候，`sharp.node` 和 `koffi.node` 被独占锁定。此时让 npm 换版本：

```
npm error errno -4082
npm error EBUSY: resource busy or locked, rename '...conpty.node' -> '...'
```

更糟的是失败之后的状态 —— **树被改坏了一半**：

```
package.json 要求 node-pty@1.0.0
实际已装 1.1.0
npm ls 报 ELSPROBLEMS
```

新版没生效、旧版也不完整。所以**升级必须停服务**。

### 2. 文件被占用时连目录都改不了名

以为可以"边跑边换目录"？不行：

```
Rename-Item  → Access to the path is denied
Directory.Move → Access to the path is denied
```

目录里有文件被打开时，整个目录改名会被拒绝。**必须先停服务**。

### 3. 整棵树很大，慢操作不能放在停机窗口里

| 操作 | 实测耗时 |
| --- | --- |
| 全新 `npm install` | **109 秒** |
| robocopy 复制整棵树 | **56 秒** |
| 同盘 rename 目录 | 瞬时（MFT 操作） |
| 停止服务 | 1–2 秒 |
| 启动到端口监听 | **8 秒** |

739 MB / 70,164 个文件。如果停机期间做安装，用户的管理界面要白屏两分钟。

## 方案：两阶段，把慢操作移出停机窗口

```
阶段 A —— 服务照常运行，用户无感（约 2 分钟）
  1. 检查磁盘剩余 ≥ 2 GB
  2. npm install --prefix <stage> @deepseek-ai/dsh@<目标版本>
  3. 校验 stage 里的版本号和 lib/bin.js
  └─ 任何一步失败：删掉 stage，系统状态没变过

阶段 B —— 停机约 2–10 秒
  4. Stop(port)，轮询确认端口释放
  5. rename   <installRoot>      → <installRoot>.old
  6. rename   <stage>/.../dsh    → <installRoot>
  7. Start(port)，等端口监听（8 秒）
  8. 请求一次页面确认 HTTP 200
  9. 成功 → 后台删除 .old 和 stage
```

关键点：**慢的下载放在服务运行时做，停机窗口里只做同盘 rename。**

## 阶段 A 必须用 `npm install -g --prefix`

这是整套流程里**最容易写错的一行**，看着多余，实则关键：

```
npm install        --prefix <stage> <pkg>   ← 错
npm install -g     --prefix <stage> <pkg>   ← 对
```

两种语义产生的目录布局完全不同：

| 命令 | 布局 |
| --- | --- |
| 不带 `-g` | 依赖**提升到 `<stage>\node_modules` 平铺** |
| 带 `-g` | 依赖嵌套在 `<stage>\node_modules\@deepseek-ai\dsh\node_modules` 里 |

真正装到 `D:\npm` 的是后者（329 个包全在嵌套层，扁平层只有 dsh 自己）。
如果拿平铺装的树去换目录，dsh 会找不到自己的依赖：

```
Error [ERR_MODULE_NOT_FOUND]: Cannot find package
'@deepseek-ai/dsh-app-boot' imported from D:\npm\...\dsh\lib\bin.js
```

而且换完之后 stage 的平铺层还留在原地，会把旧目录的依赖混进来。

**这个坑实际踩过一次**：回滚正确生效、服务没坏，但升级失败。别改回去。

顺带：带 `-g` 反而更快（58 秒 vs 109 秒）。

## 备份和损坏副本必须放在 scope 外面

```go
backupDir  = <prefix>\.dsh-web-old      ← 对
brokenDir  = <prefix>\.dsh-web-broken   ← 对

installRoot + ".old"    ← 错：变成 node_modules\@deepseek-ai\dsh.old
installRoot + ".broken" ← 错：变成 node_modules\@deepseek-ai\dsh.broken
```

放到 `@deepseek-ai` 下面的话，npm 会把它们当成**已安装的包**：

```
+-- @deepseek-ai/dsh.broken@npm:@deepseek-ai/dsh@0.2.2-rc.1
+-- @deepseek-ai/dsh@0.2.2-rc.1
```

`npm ls -g` 会列出两个 dsh，后续操作都可能被污染。npm 扫 `node_modules`
时不校验目录名的合法性，只要是目录就当包。

## 清理备份不能靠后台 goroutine

737 MB / 7 万个文件，删一遍要 **18 秒**。最初写的是：

```go
go os.RemoveAll(backup)   // 错
```

命令行模式下 `runUpdateCommand` 返回后立刻 `os.Exit`，后台 goroutine 被直接
掐掉 —— 实测过一次「升级成功」但 737 MB 备份原封不动留着。

现在改成同步删 + 12 秒上限（`cleanupWithTimeout`）：

- 删得掉最好；
- 删不掉就放弃，交给**下次升级开头**清理
  （`phaseA` 会 `RemoveAll(stage)`，`phaseB` 会 `RemoveAll(backup)`）。

这样任何情况下都不会无限堆积，也不会让用户等 18 秒。

## 阶段 A 就要校验布局

因为布局错了的代价是「跑到阶段 B 换完目录、启动失败、再回滚」，
绕一大圈才暴露。所以在 `phaseA` 末尾直接校验：

```go
nested := <staged>\node_modules\@deepseek-ai
必须有 dsh-app-boot，且嵌套 @deepseek-ai 目录数 ≥ 100
```

不满足就当场删掉 stage 报错，服务完全没被动过。

## 备份必须用唯一名字，而且删不掉不能挡路

这是实测踩出来的第三条：

**Windows 上文件被映射时，删除和目录改名都会失败**（目录重命名会检查内部
文件是否被打开）。备份里恰好有 `libvips-42.dll` 这种 native 库，一旦有 node
进程映射过它（哪怕进程已经死了、映射还没释放），

```
os.RemoveAll(backup)  → Access is denied
os.Rename(backup, x)  → Access is denied
```

定长名字 `.dsh-web-old` 在这里会把**下一次升级彻底堵死** —— 清理失败就中止，
而清理永远失败。

现在的做法：

1. 备份目录名带纳秒时间戳：`.dsh-web-old-<UnixNano>`，每次都是全新路径；
2. 开头 `listStaleBackups()` 扫一遍历史备份，`RemoveAll` 尽力删，
   **删不掉就算了，绝不阻塞本次升级**；
3. 结尾 `cleanupWithTimeout(backup, 12s)`，超时就留到下次。

实测验证：故意留一个被锁的 `.dsh-web-old` 再升级 —— 升级照常成功，
日志里只有一句「清理 ... 超时，留待下次升级处理」。

## 为什么换目录后不用修任何东西

`D:\npm\dsh.cmd` 里写的是：

```
"%dp0%\node_modules\@deepseek-ai\dsh\lib\bin.js"
```

**按路径引用，不带版本号**，所以换掉目录内容后 shim 自动指向新版。

profile 目录里那 610 个 `@deepseek-ai` 包是 junction，指向全局安装目录：

```
Substitute Name: \??\D:\npm\node_modules\@deepseek-ai\dsh\node_modules\@deepseek-ai\dsh-base
```

junction 绑的是**路径字符串**，不是目录内容。实测：

```
起点        v1
rename 后   []      ← 断链
放新目录后  v2      ← 自动指向新内容
```

所以同路径换内容后，610 个 junction 全部自动生效，**一个都不用重建**。

顺带一提：那 610 个里现在已经有 **208 个是悬空的**（指向早已不存在的包），
而 dsh 运行完全正常 —— 说明悬空 junction 不致命，升级时也不用处理。

## 失败处理

每个失败点都有确定行为，核心是**永远给用户留一个能用的东西**：

| 失败点 | 处置 |
| --- | --- |
| 网络不通 / 超时 | 自动检查静默记日志；手动检查提示"检查失败" |
| 目标版本不存在 | 阶段 A 就失败，删 stage，服务不动 |
| 磁盘不足 | 提前拒绝，提示需要多少 |
| 阶段 A npm 失败 | 删 stage，服务继续跑，报原始 npm 错误 |
| 阶段 B rename 失败 | 把旧版换回去，启动旧版，报错 |
| 新版启动失败 | **自动回滚**：Stop → 移走新版 → `.old` 换回 → 启动旧版 |
| 新版起来但 HTTP ≠ 200 | 同上回滚 |
| 回滚也失败 | 记日志，尽力把新版放回来（至少目录不空） |

回滚后 `Start` 会重新拉起旧版，用户只是看到"更新失败"，服务仍然可用。

## 版本检测

- 数据源：`<registry>/-/package/@deepseek-ai/dsh/dist-tags`，**67 字节 / 117 ms**
- 不用完整 packument（325 KB），也不用 `npm view`（要起 node 进程，850 ms）
- registry 地址按 `npm_config_registry` → `~/.npmrc` 的 `registry=` → 官方源
  依次取。用户用 npmmirror 镜像，硬编码官方源会超时

SemVer 比较是自己实现的（[version.go](../internal/updater/version.go)），因为当前版本就是预发布版：

- `0.2.2-alpha.3` < `0.2.2-rc.1` < `0.2.2`
- `rc.10 > rc.9`（数字段按数值比，**不能按字典序**）
- 非法版本一律判定为"没有更新" —— 宁可漏报，绝不提示一个不存在的更新

## 体积代价

**不要改用 `net/http`。** 实测：

| 做法 | 二进制体积 |
| --- | --- |
| 无网络能力（改造前） | 3,140,608 B |
| **WinHTTP P/Invoke（当前）** | **3,212,288 B（+68 KB）** |
| 手写 crypto/tls + HTTP 解析 | 4.27 MB（+1.3 MB） |
| `net/http` + `encoding/json` | 5.62 MB（+2.6 MB） |

`net/http` 会把 http2、DNS resolver、reflect 整条链拉进来，把当初 Go 重写
省下的体积还回去一半。`winhttp.dll` 是系统组件，走 P/Invoke 只涨 68 KB。

同理，JSON 也是手写的（[json.go](../internal/updater/json.go) 里的 `JSONTopString`）：
为了从一个 8 KB 的 package.json 里抠一个 `version` 字段而引入 `encoding/json`，
要多 264 KB，不划算。**注意 `jsonTopString` 只认深度 1 的键** —— package.json
的 `dependencies` 里也有 `@deepseek-ai/dsh` 这样的同名键，不限制深度会取错。

## 线程与交互

- 后台检查每 6 小时一次，启动后延迟 90 秒（避免和管理界面首屏抢带宽）
- 检查结果通过 `postMessage(wmUpdateReady)` 交回窗口线程；
  **`wndProc` 里绝不联网**（见 [ARCHITECTURE 线程模型](ARCHITECTURE.md#线程模型)）
- 同一个版本只弹一次气泡，按版本号去重，不重复骚扰
- 升级中菜单两项置灰，托盘图标切 busy 态
- 升级在 goroutine 里跑，`exit()` 会 close `updateStop` 收掉检查循环

## 命令行

```powershell
dsh-web.exe -CheckUpdate        # 查一次，打印结果就走
dsh-web.exe -Update             # 升到 latest
dsh-web.exe -UpdateTo 1.2.3     # 升到指定版本
dsh-web.exe -UpdateTo 1.2.3 -Force   # 允许重装同版本（修复损坏的安装）
dsh-web.exe -NoUpdateCheck      # 托盘不做后台检查
```

`-Force` 存在的理由：服务运行时 npm 换版本会把树改坏一半，这时候
"重装同版本"是唯一的修复手段，所以必须允许。

升级日志写在 `logs\update.log`（UTF-8）。用 PowerShell 的 `Get-Content`
直接看会像乱码，那是按 GBK 解码导致的，加 `-Encoding UTF8` 就正常。
