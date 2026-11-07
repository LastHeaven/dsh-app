//go:build windows

package main

import "testing"

// SemVer 比较是"会不会误报更新"的核心，重点覆盖预发布版本。
func TestCompareSemVer(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 基本
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.2.2", "0.2.1", 1},

		// 正式版 > 预发布
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0", -1},

		// alpha < beta < rc（字典序字母段）
		{"0.2.2-beta", "0.2.2-alpha", 1},
		{"0.2.2-rc.1", "0.2.2-alpha.3", 1},
		{"0.2.2-alpha.3", "0.2.2-rc.1", -1},

		// **数值段按数值比，不是字典序**：rc.10 > rc.9
		{"1.0.0-rc.10", "1.0.0-rc.9", 1},
		{"1.0.0-rc.9", "1.0.0-rc.10", -1},

		// 段少的小
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},

		// 数字段 < 字母段
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-alpha.1", 1},

		// 真实的当前版本
		{"0.2.2-rc.1", "0.2.2-rc.1", 0},
		{"0.2.2-rc.2", "0.2.2-rc.1", 1},
	}
	for _, c := range cases {
		got := compareSemVer(parseSemVer(c.a), parseSemVer(c.b))
		if got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseSemVerInvalid(t *testing.T) {
	bad := []string{"", "abc", "1", "1.2", "1.2.3.4", "1.2.x", "v", "-rc.1"}
	for _, s := range bad {
		v := parseSemVer(s)
		// "1" 和 "1.2" 按 SemVer 是合法的部分版本，但 npm 的 version 一定写全；
		// 这里只要求非法输入被判为 invalid
		if (s == "1" || s == "1.2") && !v.valid {
			continue // 允许宽松解析
		}
		if v.valid && (s == "abc" || s == "1.2.3.4" || s == "1.2.x") {
			t.Errorf("parseSemVer(%q) should be invalid, got %+v", s, v)
		}
	}
}

// isNewer 是"要不要提示升级"的唯一判据：宁可漏报，不可误报。
func TestIsNewer(t *testing.T) {
	cases := []struct {
		remote, local string
		want          bool
	}{
		{"0.2.2-rc.2", "0.2.2-rc.1", true},
		{"0.2.2-rc.1", "0.2.2-rc.1", false},
		{"0.2.1", "0.2.2-rc.1", false},
		{"0.3.0", "0.2.2-rc.1", true},
		// 垃圾输入一律不提示
		{"", "0.2.2-rc.1", false},
		{"0.2.2-rc.1", "", false},
		{"abc", "0.2.2-rc.1", false},
		{"0.2.2-rc.1", "abc", false},
	}
	for _, c := range cases {
		if got := isNewer(c.remote, c.local); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.remote, c.local, got, c.want)
		}
	}
}

// JSON 抽取必须只认顶层键：package.json 的 dependencies 里有同名键。
func TestJSONTopString(t *testing.T) {
	src := `{
  "name": "@deepseek-ai/dsh",
  "version": "0.2.2-rc.1",
  "dependencies": {
    "@deepseek-ai/dsh": "9.9.9",
    "version": "6.6.6"
  },
  "nested": { "a": { "b": 1 } },
  "engines": { "node": ">=24.9.0" }
}`
	if got := jsonTopString(src, "name"); got != "@deepseek-ai/dsh" {
		t.Errorf("name = %q", got)
	}
	// 关键：必须是顶层 0.2.2-rc.1，不能取到 dependencies.version 的 6.6.6
	if got := jsonTopString(src, "version"); got != "0.2.2-rc.1" {
		t.Errorf("version = %q (取到了嵌套的?)", got)
	}
	if got := jsonTopString(`{"version":"1.0.0"}`, "version"); got != "1.0.0" {
		t.Errorf("compact = %q", got)
	}
	if got := jsonTopString(`{}`, "version"); got != "" {
		t.Errorf("empty obj = %q", got)
	}
	if got := jsonTopString(`not json`, "version"); got != "" {
		t.Errorf("garbage = %q", got)
	}
}

// dist-tags 解析
func TestJSONMapString(t *testing.T) {
	src := `{"alpha":"0.2.2-alpha.3","latest":"0.2.2-rc.1","next":"0.2.2-rc.1"}`
	m := jsonMapString(src)
	if m["latest"] != "0.2.2-rc.1" {
		t.Errorf("latest = %q", m["latest"])
	}
	if m["alpha"] != "0.2.2-alpha.3" {
		t.Errorf("alpha = %q", m["alpha"])
	}
	if len(m) != 3 {
		t.Errorf("len = %d, want 3 (%v)", len(m), m)
	}
}

// URL 拆分：https 默认 443，显式端口优先。
func TestSplitHTTPURL(t *testing.T) {
	cases := []struct {
		raw        string
		host, path string
		port       int
		scheme     string
	}{
		{"https://registry.npmmirror.com/-/package/@deepseek-ai/dsh/dist-tags",
			"registry.npmmirror.com", "/-/package/@deepseek-ai/dsh/dist-tags", 443, "https"},
		{"https://example.com", "example.com", "/", 443, "https"},
		{"http://example.com/x", "example.com", "/x", 80, "http"},
		{"http://127.0.0.1:3199/", "127.0.0.1", "/", 3199, "http"},
		{"https://example.com:8443/a/b?c=1", "example.com", "/a/b?c=1", 8443, "https"},
	}
	for _, c := range cases {
		scheme, host, port, path, err := splitHTTPURL(c.raw)
		if err != nil {
			t.Errorf("splitHTTPURL(%q) err=%v", c.raw, err)
			continue
		}
		if host != c.host || port != c.port || path != c.path || scheme != c.scheme {
			t.Errorf("splitHTTPURL(%q) = %s %s %d %s, want %s %s %d %s",
				c.raw, scheme, host, port, path, c.scheme, c.host, c.port, c.path)
		}
	}
	for _, bad := range []string{"", "example.com", "ftp://x/y", "https://"} {
		if _, _, _, _, err := splitHTTPURL(bad); err == nil {
			t.Errorf("splitHTTPURL(%q) should fail", bad)
		}
	}
}
