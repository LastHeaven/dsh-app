//go:build windows

package service

import "testing"

// Stop 的判定是红线：只认命令行里的 --port，绝不"猜"。
// 这些用例锁住的是**两次真实事故**的回归。
func TestExplicitPort(t *testing.T) {
	cases := []struct {
		cmd  string
		port int
		ok   bool
	}{
		{`node.exe C:\...\bin.js web --port 3199 --no-open`, 3199, true},
		{`node.exe C:\...\bin.js web --port=3199`, 3199, true},
		{`node.exe C:\...\bin.js web -p 3201`, 3201, true},
		{`node.exe C:\...\bin.js web --no-open`, 0, false},
		{`node.exe C:\...\bin.js web --port abc`, 0, false},
	}
	for _, c := range cases {
		got, ok := explicitPort(c.cmd)
		if ok != c.ok || (ok && got != c.port) {
			t.Errorf("explicitPort(%q) = (%d,%v), want (%d,%v)", c.cmd, got, ok, c.port, c.ok)
		}
	}
}

// 没写 --port 的实例跑在默认 3080；这是"只按 web 匹配"会误伤 3080 的根源。
func TestListensOnPort(t *testing.T) {
	noPort := `node.exe C:\...\bin.js web --no-open`
	if !listensOnPort(noPort, DefaultPort) {
		t.Errorf("不带 --port 的实例应认定在 %d 上", DefaultPort)
	}
	if listensOnPort(noPort, 3199) {
		t.Errorf("不带 --port 的实例绝不能认定在 3199 上 —— 这就是两次事故的成因")
	}

	withPort := `node.exe C:\...\bin.js web --port 3199`
	if listensOnPort(withPort, 3080) {
		t.Errorf("显式 3199 的实例不应被认定在 3080 上")
	}
	if !listensOnPort(withPort, 3199) {
		t.Errorf("显式 3199 的实例应认定在 3199 上")
	}
}
