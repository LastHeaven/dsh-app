//go:build windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LastHeaven/dsh-app/internal/config"
	"github.com/LastHeaven/dsh-app/internal/paths"
	"github.com/LastHeaven/dsh-app/internal/service"
	"github.com/LastHeaven/dsh-app/internal/win32"
)

// stageDir 放在 npm 前缀下 —— **必须与安装目录同一个盘**，
// 跨盘 rename 会退化成 56 秒的复制，方案就失效了。
func stageDir(installRoot string) string {
	// ...\npm\node_modules\@deepseek-ai\dsh → ...\npm
	prefix := filepath.Dir(filepath.Dir(filepath.Dir(installRoot)))
	return filepath.Join(prefix, ".dsh-web-stage")
}

// backupDir 生成一个**唯一**的备份目录名。
//
// 为什么不用定长的 `.dsh-web-old`：升级中途失败后，备份里可能还有被别的
// 进程映射的 native DLL（实测 libvips-42.dll）。文件被占用时
// **删除和目录改名都会失败**，定长名字就把下一次升级堵死了。
// 用唯一名字可以让每次升级都拿得到干净路径，老备份留给 best-effort 清理。
//
// 目录必须放在 `@deepseek-ai` 作用域**外面**，否则会被 npm 当成已安装的包
// （见 PITFALLS「备份目录不能放进 scope」）。
func backupDir(installRoot string) string {
	prefix := filepath.Dir(filepath.Dir(filepath.Dir(installRoot)))
	return filepath.Join(prefix, fmt.Sprintf(".dsh-web-old-%d", time.Now().UnixNano()))
}

// listStaleBackups 列出 prefix 下所有历史备份/损坏副本目录。
func listStaleBackups(installRoot string) []string {
	prefix := filepath.Dir(filepath.Dir(filepath.Dir(installRoot)))
	f, err := os.Open(prefix)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, ".dsh-web-old") || strings.HasPrefix(n, ".dsh-web-broken") {
			out = append(out, filepath.Join(prefix, n))
		}
	}
	return out
}

// brokenDir 是回滚时用来暂存"失败的新版"的目录。
//
// **必须放在 @deepseek-ai 作用域之外**。最初写成 `<installRoot>.broken`，
// 也就是 `node_modules\@deepseek-ai\dsh.broken`，结果 npm 把它当成了另一个
// 合法的包：
//
//	+-- @deepseek-ai/dsh.broken@npm:@deepseek-ai/dsh@0.2.2-rc.1
//
// 看目录名（`dsh.broken`）不像包名，但 npm 扫 node_modules 时不看名字合法性，
// 只要是目录就当成已安装的包。放回 scope 外面就没这个问题。
func brokenDir(installRoot string) string {
	prefix := filepath.Dir(filepath.Dir(filepath.Dir(installRoot)))
	return filepath.Join(prefix, ".dsh-web-broken")
}

// phaseA 下载新版到 stage，全程服务照常运行。
//
// 失败时把 stage 删干净即可 —— 系统里什么都没变过，这是本方案
// 相对"原地升级"的核心优势。
func phaseA(installRoot, version string, logf Logf) error {
	stage := stageDir(installRoot)

	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("清理暂存目录失败：%w", err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("创建暂存目录失败：%w", err)
	}

	// 磁盘空间预检：新树约 739 MB，留 2 GB 余量
	if free, err := win32.DiskFreeBytes(stage); err == nil && free < 2<<30 {
		return fmt.Errorf("磁盘剩余空间不足（需 2 GB，当前 %.1f GB）", float64(free)/(1<<30))
	}

	logf("开始下载 @%s（服务保持运行）", version)
	if err := runNpmInstall(stage, version); err != nil {
		os.RemoveAll(stage)
		return err
	}

	// 校验：版本号必须真的对得上，不能凭 npm 的退出码
	staged := filepath.Join(stage, "node_modules", "@deepseek-ai", "dsh")
	got := JSONTopString(paths.SafeRead(filepath.Join(staged, "package.json")), "version")
	if got != version {
		os.RemoveAll(stage)
		return fmt.Errorf("下载的版本不对：期望 %s，实际 %q", version, got)
	}
	if !paths.FileExists(filepath.Join(staged, "lib", "bin.js")) {
		os.RemoveAll(stage)
		return errors.New("下载的包里缺少 lib/bin.js")
	}

	// **布局校验：依赖必须嵌套在包自己的 node_modules 里。**
	// 少了这一条，`npm install` 漏掉 `-g` 时阶段 A 会"成功"，一直拖到
	// 阶段 B 换完目录、启动失败才暴露（真实踩过）。提前拦住便宜得多。
	nested := filepath.Join(staged, "node_modules", "@deepseek-ai")
	if !paths.DirExists(filepath.Join(nested, "dsh-app-boot")) {
		os.RemoveAll(stage)
		return errors.New("下载的包依赖布局不对（缺嵌套的 dsh-app-boot），" +
			"多半是 npm install 少了 -g")
	}
	if n := len(dirEntries(nested)); n < 100 {
		os.RemoveAll(stage)
		return fmt.Errorf("下载的包依赖不全：嵌套 @deepseek-ai 只有 %d 个", n)
	}

	logf("下载完成并校验通过")
	return nil
}

// dirEntries 列出目录下的子目录（用于布局校验）。
func dirEntries(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil
	}
	return names
}

// runNpmInstall 调 npm 装到 stage。
//
// **必须带 `-g`**。这看着多余，实际是整个升级流程的关键：
//
//   - `npm install --prefix <stage> <pkg>` 是"本地安装"语义，依赖会被
//     **提升到 `<stage>\node_modules` 平铺**；
//   - `npm install -g --prefix <stage> <pkg>` 才是"全局安装"语义，目录布局
//     与真正装到 `D:\npm` 时完全一致（依赖嵌套在 `@deepseek-ai\dsh\node_modules` 里）。
//
// 两者布局不同，直接换目录就会出真实事故：平铺装出来的树被搬到
// `node_modules\@deepseek-ai\dsh` 后，dsh 找不到自己的依赖，启动报
//
//	Error [ERR_MODULE_NOT_FOUND]: Cannot find package
//	'@deepseek-ai/dsh-app-boot' imported from ...\dsh\lib\bin.js
//
// 这个坑实际踩到过一次（回滚正确生效，服务没坏），别再改回去。
func runNpmInstall(stage, version string) error {
	npm := "npm.cmd"
	if _, err := exec.LookPath(npm); err != nil {
		return errors.New("未找到 npm，请确认 Node.js 已安装并加入 PATH")
	}

	cmd := exec.Command(npm, "install", "-g",
		"--prefix", stage,
		PackageName+"@"+version,
		"--no-audit", "--no-fund", "--loglevel=error")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: win32.CreateNoWindow,
	}

	done := make(chan error, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := paths.TrimForMsg(string(out), 400)
			if msg == "" {
				msg = err.Error()
			}
			done <- fmt.Errorf("npm install 失败：%s", msg)
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(InstallTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return errors.New("npm install 超时（15 分钟）")
	}
}

// phaseB 停机换目录。这一步只有几百毫秒到几秒的停机。
func phaseB(installRoot, version string, opt *config.Options, logf Logf) error {
	stagePkg := filepath.Join(stageDir(installRoot), "node_modules", "@deepseek-ai", "dsh")
	backup := backupDir(installRoot)

	logf("停止端口 %d 上的服务", opt.Port)
	service.Stop(opt.Port)

	// 等端口真的释放，否则 rename 会因为文件被占用而失败
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}
	if paths.IsPortListening(opt.Port) {
		return fmt.Errorf("端口 %d 仍被占用，取消升级", opt.Port)
	}

	// 上一次失败可能留下备份目录。**清不掉绝不能当中止理由**：
	// 备份里的 native DLL 可能还被别的进程映射着（实测 libvips-42.dll
	// 报 Access is denied），而且文件被占用时连目录改名都会被拒
	// （Windows 的目录重命名会检查内部文件是否被打开）。
	//
	// 所以定长名字在这里是行不通的：删不掉、也挪不开。改成**每次用唯一名字**，
	// 老备份就地留着，由 best-effort 清理，绝不阻塞本次升级。
	for _, stale := range listStaleBackups(installRoot) {
		_ = os.RemoveAll(stale) // 删不掉就算，不影响本次
	}

	logf("切换安装目录")
	if err := os.Rename(installRoot, backup); err != nil {
		return fmt.Errorf("无法移走旧版本（文件仍被占用？）：%w", err)
	}
	if err := os.Rename(stagePkg, installRoot); err != nil {
		// 换不进去就把旧的放回来，别让用户没得用
		_ = os.Rename(backup, installRoot)
		return fmt.Errorf("无法放入新版本：%w", err)
	}

	logf("启动新版本")
	if err := verifyNewVersion(installRoot, version, opt, logf); err != nil {
		logf("新版本验证失败，开始回滚：%v", err)
		rollback(installRoot, backup, opt, logf)
		return fmt.Errorf("新版本启动失败，已回滚到旧版：%w", err)
	}

	logf("升级成功")

	// 清理旧副本和暂存目录。
	//
	// **不能简单 `go os.RemoveAll(...)`**：737 MB / 7 万个文件删一遍要 18 秒，
	// 而命令行模式跑完就 `os.Exit`，后台 goroutine 会被直接掐掉，
	// 结果备份原封不动留着（实测过一次：升级"成功"但 737 MB 残留）。
	//
	// 所以这里同步删、但设上限：删得掉最好，删不掉就交给下次升级开头清理
	// （phaseA 会 RemoveAll(stage)，phaseB 会 RemoveAll(backup)）。
	cleanupWithTimeout(backup, 12*time.Second, logf)
	cleanupWithTimeout(stageDir(installRoot), 12*time.Second, logf)
	return nil
}

// cleanupWithTimeout 限时删除，超时就放弃（下次升级会再清）。
func cleanupWithTimeout(dir string, limit time.Duration, logf Logf) {
	if dir == "" || !paths.DirExists(dir) {
		return
	}
	done := make(chan error, 1)
	go func() { done <- os.RemoveAll(dir) }()

	select {
	case err := <-done:
		if err != nil {
			logf("清理 %s 失败：%v（下次升级会重试）", filepath.Base(dir), err)
		}
	case <-time.After(limit):
		logf("清理 %s 超时，留待下次升级处理", filepath.Base(dir))
	}
}

// verifyNewVersion 启动新版并确认真的能用。
func verifyNewVersion(installRoot, version string, opt *config.Options, logf Logf) error {
	if got := LocalVersion(installRoot); got != version {
		return fmt.Errorf("切换后版本号是 %q，不是 %q", got, version)
	}

	url, err := service.Start(opt)
	if err != nil {
		return err
	}
	// Start 内部已经等过端口监听。再要一次 HTTP 200 才算真起来了：
	// 端口开着但页面 500 也是失败，必须验证到底。
	if u := strings.TrimSpace(url); u != "" {
		st, _, err := win32.WinHTTPGet(u, 15000)
		if err != nil {
			return fmt.Errorf("页面无法访问：%w", err)
		}
		if st != 200 {
			return fmt.Errorf("页面返回 HTTP %d", st)
		}
	}
	return nil
}

// rollback 把旧版本放回去并重新拉起。
func rollback(installRoot, backup string, opt *config.Options, logf Logf) {
	service.Stop(opt.Port)

	broken := brokenDir(installRoot)
	if err := os.RemoveAll(broken); err != nil {
		logf("回滚：清理上次的损坏副本失败：%v", err)
	}
	if err := os.Rename(installRoot, broken); err != nil {
		logf("回滚：无法移走新版：%v", err)
		return
	}
	if err := os.Rename(backup, installRoot); err != nil {
		logf("回滚：无法恢复旧版：%v", err)
		// 尽力把新版放回来，至少别让目录空着
		_ = os.Rename(broken, installRoot)
		return
	}
	if _, err := service.Start(opt); err != nil {
		logf("回滚：旧版启动也失败了：%v", err)
	}
	go os.RemoveAll(broken)
}
