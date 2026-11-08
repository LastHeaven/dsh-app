//go:build windows

// 更新检测与升级入口。
//
// 设计依据（都是实测结论，改之前先读 docs/UPDATE.md）：
//
//  1. dsh 是 npm 全局包，自身**没有** update 命令，升级只能走 npm。
//  2. 服务运行时 sharp.node / koffi.node 被独占锁定，此时 npm 换版本会报
//     EBUSY 并把安装树改坏一半（package.json 要求的版本与实际装的版本不一致）。
//     所以升级期间必须停服务，且不能用「原地 npm install」。
//  3. 整棵安装树 739 MB / 7 万个文件：复制要 56 秒，npm 全新安装要 109 秒。
//     因此「下载」放服务运行期间做（阶段 A，用户无感），
//     真正的切换只用同盘 rename（阶段 B，瞬时）。
//  4. profile 目录里那 610 个 @deepseek-ai 包全是 junction，指向全局安装目录。
//     junction 绑的是**路径字符串**，同路径换内容后自动指向新版，不需要重建。
package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LastHeaven/dsh-app/internal/applog"
	"github.com/LastHeaven/dsh-app/internal/config"
	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

const (
	// PackageName 是 dsh 的 npm 包名。
	PackageName = "@deepseek-ai/dsh"

	// 首次检查延迟：太早点会和管理界面首屏加载抢带宽
	InitialDelay = 90 * time.Second

	// 检查间隔：6 小时
	Interval = 6 * time.Hour

	// 网络超时
	HTTPTimeoutMs = 20000

	// 阶段 A 安装超时：实测 109 秒，给足余量
	InstallTimeout = 15 * time.Minute
)

// Logf 是升级流程的进度回调（命令行写控制台，托盘写日志）。
type Logf func(format string, a ...interface{})

// State 是一次检查的结果。
type State struct {
	Current   string
	Latest    string
	HasUpdate bool
	CheckedAt time.Time
	Err       string
}

// Log 写更新日志到 logs\update.log。
func Log(format string, a ...interface{}) {
	applog.UpdateLog(format, a...)
}

// InstallRoot 定位 dsh 安装目录。
//
// **这里绝不猜路径**（和 -Stop 只认 --port 是同一类纪律）：必须由
// ResolveDshBin() 得到的 lib\bin.js 反推，且反推出来的目录里 package.json
// 的 name 必须真的是 @deepseek-ai/dsh。对不上就返回空 —— 宁可提示
// "未找到 dsh 安装"，也不能对一个来路不明的目录执行 rename。
func InstallRoot() string {
	bin := paths.ResolveDshBin()
	if bin == "" {
		return ""
	}
	// ...\@deepseek-ai\dsh\lib\bin.js → ...\@deepseek-ai\dsh
	root := filepath.Dir(filepath.Dir(bin))
	if !paths.FileExists(filepath.Join(root, "package.json")) {
		return ""
	}
	if v := JSONTopString(paths.SafeRead(filepath.Join(root, "package.json")), "name"); v != PackageName {
		return ""
	}
	return root
}

// LocalVersion 读本地安装的版本号。
//
// 注意：dsh 顶层 package.json 的 version 才是 npm 包版本；
// 运行时的 `dsh --version` 读的是 dsh-app-boot 的 package.json。
// 检测更新要用前者。
func LocalVersion(root string) string {
	if root == "" {
		return ""
	}
	return JSONTopString(paths.SafeRead(filepath.Join(root, "package.json")), "version")
}

// ---------------------------------------------------------------- 检测

// RegistryURL 取 npm registry 地址。
//
// 依次看：npm_config_registry 环境变量 → ~/.npmrc 的 registry= → 官方源。
// 用户用的是 npmmirror 镜像，硬编码官方源在国内会超时。
func RegistryURL() string {
	if v := strings.TrimSpace(os.Getenv("npm_config_registry")); v != "" {
		return strings.TrimRight(v, "/")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		for _, line := range strings.Split(paths.SafeRead(filepath.Join(home, ".npmrc")), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "registry=") {
				if v := strings.TrimSpace(strings.TrimPrefix(line, "registry=")); v != "" {
					return strings.TrimRight(v, "/")
				}
			}
		}
	}
	return "https://registry.npmjs.org"
}

// DistTagsURL 是查最新版的最小端点：67 字节，比完整 packument（325 KB）快得多。
func DistTagsURL(registry string) string {
	return registry + "/-/package/" + PackageName + "/dist-tags"
}

// fetchDistTags 拿 {"latest":...} 等标签。
//
// 先试轻量的 dist-tags 端点；若镜像不认这个路径，回退到完整 packument。
// 两条路都失败才算失败。
func fetchDistTags(registry string) (map[string]string, error) {
	code, body, err := win32.WinHTTPGet(DistTagsURL(registry), HTTPTimeoutMs)
	if err == nil && code == 200 {
		if tags := JSONMapString(body); len(tags) > 0 {
			return tags, nil
		}
	}

	// 回退：完整 packument。体积大但所有 registry 都支持。
	pkgURL := registry + "/" + strings.Replace(PackageName, "/", "%2f", 1)
	code2, body2, err2 := win32.WinHTTPGet(pkgURL, HTTPTimeoutMs)
	if err2 == nil && code2 == 200 {
		if tags := JSONMapString(body2); len(tags) > 0 {
			return tags, nil
		}
		// packument 里的 dist-tags 是嵌套的，单独抠
		if latest := JSONTopString(body2, "latest"); latest != "" {
			return map[string]string{"latest": latest}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if err2 != nil {
		return nil, err2
	}
	return nil, fmt.Errorf("registry 返回了无法解析的内容（HTTP %d）", code)
}

// Check 查一次最新版本。
func Check() State {
	st := State{CheckedAt: time.Now()}

	root := InstallRoot()
	if root == "" {
		st.Err = "未找到 dsh 安装目录"
		return st
	}
	st.Current = LocalVersion(root)
	if st.Current == "" {
		st.Err = "读不到本地 dsh 版本"
		return st
	}

	tags, err := fetchDistTags(RegistryURL())
	if err != nil {
		st.Err = paths.TrimForMsg(err.Error(), 160)
		return st
	}
	latest := tags["latest"]
	if latest == "" {
		st.Err = "registry 没有返回 latest 标签"
		return st
	}
	st.Latest = latest
	st.HasUpdate = IsNewer(latest, st.Current)
	return st
}

// ---------------------------------------------------------------- 升级

// Run 执行完整升级流程。
//
// Force 表示允许重装同版本：这是修复「安装树被 npm 改坏一半」的唯一手段
// （实测过：服务运行时 npm 换版本会 EBUSY，package.json 要求的版本和
// 实际装上的版本不一致）。默认仍然拒绝同版本，防止误触发整棵树重装。
func Run(version string, opt *config.Options, logf Logf) error {
	root := InstallRoot()
	if root == "" {
		return errors.New("未找到 dsh 安装目录")
	}
	if version == "" || !ParseSemVer(version).Valid() {
		return fmt.Errorf("无效的目标版本：%q", version)
	}
	cur := LocalVersion(root)
	if version == cur && !opt.Force {
		return fmt.Errorf("本地已是 %s，无需重装（要强制重装请加 -Force）", cur)
	}
	if version != cur && !IsNewer(version, cur) {
		return fmt.Errorf("当前 %s 比目标 %s 新，拒绝降级（要强制请加 -Force）", cur, version)
	}

	if err := phaseA(root, version, logf); err != nil {
		return err
	}
	return phaseB(root, version, opt, logf)
}
