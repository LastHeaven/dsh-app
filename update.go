//go:build windows

// 更新检测与升级。
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
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	dshPackageName = "@deepseek-ai/dsh"

	// 首次检查延迟：太早点会和管理界面首屏加载抢带宽
	updateInitialDelay = 90 * time.Second

	// 检查间隔：6 小时
	updateInterval = 6 * time.Hour

	// 网络超时
	updateHTTPTimeoutMs = 20000

	// 阶段 A 安装超时：实测 109 秒，给足余量
	updateInstallTimeout = 15 * time.Minute
)

// updateState 是一次检查的结果。
type updateState struct {
	Current   string
	Latest    string
	HasUpdate bool
	CheckedAt time.Time
	Err       string
}

// dshInstallRoot 定位 dsh 安装目录。
//
// **这里绝不猜路径**（和 -Stop 只认 --port 是同一类纪律）：必须由
// ResolveDshBin() 得到的 lib\bin.js 反推，且反推出来的目录里 package.json
// 的 name 必须真的是 @deepseek-ai/dsh。对不上就返回空 —— 宁可提示
// "未找到 dsh 安装"，也不能对一个来路不明的目录执行 rename。
func dshInstallRoot() string {
	bin := Paths.ResolveDshBin()
	if bin == "" {
		return ""
	}
	// ...\@deepseek-ai\dsh\lib\bin.js → ...\@deepseek-ai\dsh
	root := filepath.Dir(filepath.Dir(bin))
	if !fileExists(filepath.Join(root, "package.json")) {
		return ""
	}
	if v := jsonTopString(safeRead(filepath.Join(root, "package.json")), "name"); v != dshPackageName {
		return ""
	}
	return root
}

// localDshVersion 读本地安装的版本号。
//
// 注意：dsh 顶层 package.json 的 version 才是 npm 包版本；
// 运行时的 `dsh --version` 读的是 dsh-app-boot 的 package.json。
// 检测更新要用前者。
func localDshVersion(root string) string {
	if root == "" {
		return ""
	}
	return jsonTopString(safeRead(filepath.Join(root, "package.json")), "version")
}

// ------------------------------------------------------------------ JSON

// jsonTopString 取顶层对象里某个键的字符串值。
//
// **不引 encoding/json**：实测它让二进制多 264 KB，而我们只需要从一个
// 8 KB 的 package.json 里抠一个字段。
//
// 只认深度 1 的键 —— package.json 的 dependencies 里也可能出现 "@deepseek-ai/dsh"
// 这样的同名键，不限制深度会取错值。
func jsonTopString(s, key string) string {
	i := 0
	skipWS := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
	}

	// 定位到最外层 '{'
	for i < len(s) && s[i] != '{' {
		i++
	}
	if i >= len(s) {
		return ""
	}
	i++ // 跳过 '{'

	for i < len(s) {
		skipWS()
		if i >= len(s) || s[i] == '}' {
			return ""
		}
		if s[i] != '"' {
			return "" // 结构异常，别再猜
		}
		k, next, ok := scanJSONString(s, i)
		if !ok {
			return ""
		}
		i = next
		skipWS()
		if i >= len(s) || s[i] != ':' {
			return ""
		}
		i++
		skipWS()

		if k == key {
			if i < len(s) && s[i] == '"' {
				v, _, ok := scanJSONString(s, i)
				if ok {
					return v
				}
				return ""
			}
			// 非字符串值（数字/布尔），截到分隔符为止
			j := i
			for j < len(s) && s[j] != ',' && s[j] != '}' && s[j] != '\n' {
				j++
			}
			return strings.TrimSpace(s[i:j])
		}

		// 跳过这个值
		ni, ok := skipJSONValue(s, i)
		if !ok {
			return ""
		}
		i = ni
		skipWS()
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		return ""
	}
	return ""
}

// skipJSONValue 跳过一个 JSON 值，返回其后的位置。
func skipJSONValue(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	switch s[i] {
	case '"':
		_, next, ok := scanJSONString(s, i)
		return next, ok
	case '{', '[':
		open := s[i]
		closeB := byte('}')
		if open == '[' {
			closeB = ']'
		}
		depth := 0
		for i < len(s) {
			switch s[i] {
			case '"':
				_, next, ok := scanJSONString(s, i)
				if !ok {
					return i, false
				}
				i = next
				continue
			case open:
				depth++
			case closeB:
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return i, false
	default:
		// 数字/true/false/null
		j := i
		for j < len(s) && s[j] != ',' && s[j] != '}' && s[j] != ']' &&
			s[j] != '\n' && s[j] != ' ' && s[j] != '\t' && s[j] != '\r' {
			j++
		}
		if j == i {
			return i, false
		}
		return j, true
	}
}

// jsonMapString 解析扁平的 {"a":"b",...}，够 dist-tags 用。
func jsonMapString(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		if s[i] != '"' {
			i++
			continue
		}
		k, ni, ok := scanJSONString(s, i)
		if !ok {
			break
		}
		i = ni
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n' || s[i] == ':') {
			i++
		}
		if i >= len(s) || s[i] != '"' {
			continue
		}
		v, ni2, ok := scanJSONString(s, i)
		if !ok {
			break
		}
		out[k] = v
		i = ni2
	}
	return out
}

func scanJSONString(s string, start int) (string, int, bool) {
	if start >= len(s) || s[start] != '"' {
		return "", start, false
	}
	var b strings.Builder
	i := start + 1
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '"' {
			return b.String(), i + 1, true
		}
		b.WriteByte(c)
		i++
	}
	return "", start, false
}

// ------------------------------------------------------------------ 检测

// registryURL 取 npm registry 地址。
//
// 依次看：npm_config_registry 环境变量 → ~/.npmrc 的 registry= → 官方源。
// 用户用的是 npmmirror 镜像，硬编码官方源在国内会超时。
func registryURL() string {
	if v := strings.TrimSpace(os.Getenv("npm_config_registry")); v != "" {
		return strings.TrimRight(v, "/")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		for _, line := range strings.Split(safeRead(filepath.Join(home, ".npmrc")), "\n") {
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

// distTagsURL 是查最新版的最小端点：67 字节，比完整 packument（325 KB）快得多。
func distTagsURL(registry string) string {
	return registry + "/-/package/" + dshPackageName + "/dist-tags"
}

// fetchDistTags 拿 {"latest":...} 等标签。
//
// 先试轻量的 dist-tags 端点；若镜像不认这个路径，回退到完整 packument。
// 两条路都失败才算失败。
func fetchDistTags(registry string) (map[string]string, error) {
	code, body, err := winhttpGet(distTagsURL(registry), updateHTTPTimeoutMs)
	if err == nil && code == 200 {
		if tags := jsonMapString(body); len(tags) > 0 {
			return tags, nil
		}
	}

	// 回退：完整 packument。体积大但所有 registry 都支持。
	pkgURL := registry + "/" + strings.Replace(dshPackageName, "/", "%2f", 1)
	code2, body2, err2 := winhttpGet(pkgURL, updateHTTPTimeoutMs)
	if err2 == nil && code2 == 200 {
		if tags := jsonMapString(body2); len(tags) > 0 {
			return tags, nil
		}
		// packument 里的 dist-tags 是嵌套的，单独抠
		if latest := jsonTopString(body2, "latest"); latest != "" {
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

// checkUpdate 查一次最新版本。
func checkUpdate() updateState {
	st := updateState{CheckedAt: time.Now()}

	root := dshInstallRoot()
	if root == "" {
		st.Err = "未找到 dsh 安装目录"
		return st
	}
	st.Current = localDshVersion(root)
	if st.Current == "" {
		st.Err = "读不到本地 dsh 版本"
		return st
	}

	tags, err := fetchDistTags(registryURL())
	if err != nil {
		st.Err = trimForMsg(err.Error(), 160)
		return st
	}
	latest := tags["latest"]
	if latest == "" {
		st.Err = "registry 没有返回 latest 标签"
		return st
	}
	st.Latest = latest
	st.HasUpdate = isNewer(latest, st.Current)
	return st
}

// ------------------------------------------------------------------ 升级

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
func phaseA(installRoot, version string, logf func(string, ...interface{})) error {
	stage := stageDir(installRoot)

	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("清理暂存目录失败：%w", err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("创建暂存目录失败：%w", err)
	}

	// 磁盘空间预检：新树约 739 MB，留 2 GB 余量
	if free, err := diskFreeBytes(stage); err == nil && free < 2<<30 {
		return fmt.Errorf("磁盘剩余空间不足（需 2 GB，当前 %.1f GB）", float64(free)/(1<<30))
	}

	logf("开始下载 @%s（服务保持运行）", version)
	if err := runNpmInstall(stage, version); err != nil {
		os.RemoveAll(stage)
		return err
	}

	// 校验：版本号必须真的对得上，不能凭 npm 的退出码
	staged := filepath.Join(stage, "node_modules", "@deepseek-ai", "dsh")
	got := jsonTopString(safeRead(filepath.Join(staged, "package.json")), "version")
	if got != version {
		os.RemoveAll(stage)
		return fmt.Errorf("下载的版本不对：期望 %s，实际 %q", version, got)
	}
	if !fileExists(filepath.Join(staged, "lib", "bin.js")) {
		os.RemoveAll(stage)
		return errors.New("下载的包里缺少 lib/bin.js")
	}

	// **布局校验：依赖必须嵌套在包自己的 node_modules 里。**
	// 少了这一条，`npm install` 漏掉 `-g` 时阶段 A 会"成功"，一直拖到
	// 阶段 B 换完目录、启动失败才暴露（真实踩过）。提前拦住便宜得多。
	nested := filepath.Join(staged, "node_modules", "@deepseek-ai")
	if !dirExists(filepath.Join(nested, "dsh-app-boot")) {
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

// dirEntries 列出目录下的子目录数量（用于布局校验）。
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
// 而且换目录后 stage 的平铺层还残留在原地，把旧目录的依赖混进来。
// 这个坑实际踩到过一次（回滚正确生效，服务没坏），别再改回去。
func runNpmInstall(stage, version string) error {
	npm := "npm.cmd"
	if _, err := exec.LookPath(npm); err != nil {
		return errors.New("未找到 npm，请确认 Node.js 已安装并加入 PATH")
	}

	cmd := exec.Command(npm, "install", "-g",
		"--prefix", stage,
		dshPackageName+"@"+version,
		"--no-audit", "--no-fund", "--loglevel=error")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}

	done := make(chan error, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := trimForMsg(string(out), 400)
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
	case <-time.After(updateInstallTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return errors.New("npm install 超时（15 分钟）")
	}
}

// phaseB 停机换目录。这一步只有几百毫秒到几秒的停机。
func phaseB(installRoot, version string, opt *Options, logf func(string, ...interface{})) error {
	stagePkg := filepath.Join(stageDir(installRoot), "node_modules", "@deepseek-ai", "dsh")
	backup := backupDir(installRoot)

	logf("停止端口 %d 上的服务", opt.Port)
	Stop(opt.Port)

	// 等端口真的释放，否则 rename 会因为文件被占用而失败
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && Paths.IsPortListening(opt.Port) {
		time.Sleep(300 * time.Millisecond)
	}
	if Paths.IsPortListening(opt.Port) {
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
func cleanupWithTimeout(dir string, limit time.Duration, logf func(string, ...interface{})) {
	if dir == "" || !dirExists(dir) {
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
func verifyNewVersion(installRoot, version string, opt *Options, logf func(string, ...interface{})) error {
	if got := localDshVersion(installRoot); got != version {
		return fmt.Errorf("切换后版本号是 %q，不是 %q", got, version)
	}

	url, err := Start(opt)
	if err != nil {
		return err
	}
	// Start 内部已经等过端口监听。再要一次 HTTP 200 才算真起来了：
	// 端口开着但页面 500 也是失败，必须验证到底。
	if u := strings.TrimSpace(url); u != "" {
		st, _, err := winhttpGet(u, 15000)
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
func rollback(installRoot, backup string, opt *Options, logf func(string, ...interface{})) {
	Stop(opt.Port)

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
	if _, err := Start(opt); err != nil {
		logf("回滚：旧版启动也失败了：%v", err)
	}
	go os.RemoveAll(broken)
}

// updateLog 写更新日志，和现有日志同目录。
//
// 内容是 UTF-8 字节（os.File.WriteString 不做编码转换）。
// 注意：用 PowerShell 的 Get-Content 看它会像乱码，那是按 GBK 解码导致的，
// 请用 Get-Content -Encoding UTF8 或直接看日志工具，不要以为文件坏了。
func updateLog(format string, a ...interface{}) {
	dir := Paths.LogDir()
	_ = os.MkdirAll(dir, 0o755)

	t := time.Now()
	line := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d  ",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()) +
		fmt.Sprintf(format, a...) + "\r\n"

	f, err := os.OpenFile(filepath.Join(dir, "update.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// runUpdate 执行完整升级流程。
//
// force 表示允许重装同版本：这是修复「安装树被 npm 改坏一半」的唯一手段
// （实测过：服务运行时 npm 换版本会 EBUSY，package.json 要求的版本和
// 实际装上的版本不一致）。默认仍然拒绝同版本，防止误触发整棵树重装。
func runUpdate(version string, opt *Options, logf func(string, ...interface{})) error {
	root := dshInstallRoot()
	if root == "" {
		return errors.New("未找到 dsh 安装目录")
	}
	if version == "" || !parseSemVer(version).valid {
		return fmt.Errorf("无效的目标版本：%q", version)
	}
	cur := localDshVersion(root)
	if version == cur && !opt.Force {
		return fmt.Errorf("本地已是 %s，无需重装（要强制重装请加 -Force）", cur)
	}
	if version != cur && !isNewer(version, cur) {
		return fmt.Errorf("当前 %s 比目标 %s 新，拒绝降级（要强制请加 -Force）", cur, version)
	}

	if err := phaseA(root, version, logf); err != nil {
		return err
	}
	return phaseB(root, version, opt, logf)
}

// nodeVersion 读当前 node 版本，用于 engines 提示。
func nodeVersion() string {
	node := Paths.ResolveNode()
	if node == "" {
		return ""
	}
	cmd := exec.Command(node, "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"))
}

// parseIntSafe 是个小工具，避免调用处到处写 strconv。
func parseIntSafe(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}
