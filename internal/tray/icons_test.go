//go:build windows

package tray

import (
	"os"
	"testing"
)

// 托盘图标是 go:embed 进二进制的，一旦 embed 路径写错、或者图标文件没进仓库，
// exe 编译是能过的，运行时才发现通知区没图标。这个测试把这件事挪到测试阶段。
//
// 它只做「解出临时文件 + LoadImageW」，不创建窗口、不碰互斥体，
// 因此可以在已有托盘运行的情况下安全执行。
func TestLoadIcons(t *testing.T) {
	s := loadIcons()
	defer s.close()

	if s.running == 0 {
		t.Errorf("running 图标加载失败")
	}
	if s.stopped == 0 {
		t.Errorf("stopped 图标加载失败")
	}
	if s.busy == 0 {
		t.Errorf("busy 图标加载失败")
	}
	// 三态必须互不相同，否则状态变化在通知区里看不出来
	if s.running == s.stopped || s.running == s.busy || s.stopped == s.busy {
		t.Errorf("三态图标句柄重复：running=%d stopped=%d busy=%d",
			s.running, s.stopped, s.busy)
	}
}

// extractIcon 的文件名带内容哈希：同内容复用、不同内容不覆盖；
// 而且只要这次是**真的解出了一份新文件**，CleanupTemp 就必须能清掉它。
//
// 注意不能直接测"解出后 CleanupTemp 就没了"：%TEMP% 里若已存在同哈希文件，
// extractIcon 会走复用分支（这是刻意的，避免每次启动都重写），
// 那条路径本来就不登记、也不该被删。所以要先把文件删掉再解。
func TestExtractIconStablePath(t *testing.T) {
	p1, err := extractIcon("running.ico")
	if err != nil {
		t.Fatalf("extractIcon: %v", err)
	}
	p2, err := extractIcon("running.ico")
	if err != nil {
		t.Fatalf("extractIcon: %v", err)
	}
	if p1 != p2 {
		t.Errorf("同一图标的两次解析应得到同一路径：%s vs %s", p1, p2)
	}

	// 制造"需要重新解出"的前提，验证登记与清理这条链路
	CleanupTemp()
	if err := os.Remove(p1); err != nil && !os.IsNotExist(err) {
		t.Fatalf("准备测试时删不掉临时文件：%v", err)
	}
	fresh, err := extractIcon("running.ico")
	if err != nil {
		t.Fatalf("extractIcon: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("解出的临时图标不存在：%v", err)
	}
	CleanupTemp()
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("CleanupTemp 之后刚解出的临时文件应已删除")
	}
}

// 托盘文案有 63 字节上限（NOTIFYICONDATAW.szTip 是 64 个 wchar），
// 截断必须按字符切，不能把汉字劈成半个。
func TestTruncateBytes(t *testing.T) {
	if got := truncateBytes("dsh web", 63); got != "dsh web" {
		t.Errorf("短文本不应被改：%q", got)
	}
	long := "dsh web：运行中\n端口 3080 dsh web：运行中\n端口 3080"
	got := truncateBytes(long, 63)
	if len(got) > 63 {
		t.Errorf("截断后仍超 63 字节：%d", len(got))
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Errorf("截断把汉字切坏了：%q", got)
		}
	}
}
