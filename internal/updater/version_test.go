//go:build windows

package updater

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
		got := CompareSemVer(ParseSemVer(c.a), ParseSemVer(c.b))
		if got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseSemVerInvalid(t *testing.T) {
	bad := []string{"", "abc", "1", "1.2", "1.2.3.4", "1.2.x", "v", "-rc.1"}
	for _, s := range bad {
		v := ParseSemVer(s)
		// "1" 和 "1.2" 按 SemVer 是合法的部分版本，但 npm 的 version 一定写全；
		// 这里只要求非法输入被判为 invalid
		if (s == "1" || s == "1.2") && !v.Valid() {
			continue // 允许宽松解析
		}
		if v.Valid() && (s == "abc" || s == "1.2.3.4" || s == "1.2.x") {
			t.Errorf("ParseSemVer(%q) should be invalid, got %+v", s, v)
		}
	}
}

// IsNewer 是"要不要提示升级"的唯一判据：宁可漏报，不可误报。
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
		if got := IsNewer(c.remote, c.local); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.remote, c.local, got, c.want)
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
	if got := JSONTopString(src, "name"); got != "@deepseek-ai/dsh" {
		t.Errorf("name = %q", got)
	}
	// 关键：必须是顶层 0.2.2-rc.1，不能取到 dependencies.version 的 6.6.6
	if got := JSONTopString(src, "version"); got != "0.2.2-rc.1" {
		t.Errorf("version = %q (取到了嵌套的?)", got)
	}
	if got := JSONTopString(`{"version":"1.0.0"}`, "version"); got != "1.0.0" {
		t.Errorf("compact = %q", got)
	}
	if got := JSONTopString(`{}`, "version"); got != "" {
		t.Errorf("empty obj = %q", got)
	}
	if got := JSONTopString(`not json`, "version"); got != "" {
		t.Errorf("garbage = %q", got)
	}
}

// dist-tags 解析
func TestJSONMapString(t *testing.T) {
	src := `{"alpha":"0.2.2-alpha.3","latest":"0.2.2-rc.1","next":"0.2.2-rc.1"}`
	m := JSONMapString(src)
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

// DistTagsURL 必须命中轻量端点，不是完整 packument。
func TestDistTagsURL(t *testing.T) {
	got := DistTagsURL("https://registry.npmmirror.com")
	want := "https://registry.npmmirror.com/-/package/@deepseek-ai/dsh/dist-tags"
	if got != want {
		t.Errorf("DistTagsURL = %q, want %q", got, want)
	}
}
