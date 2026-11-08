//go:build windows

package cli

import "testing"

func TestParseOptions(t *testing.T) {
	o := ParseOptions([]string{"-Port", "3199", "-NoBrowser", "-KeepExisting"})
	if o.Port != 3199 {
		t.Errorf("Port = %d, want 3199", o.Port)
	}
	if !o.NoBrowser || !o.KeepExisting {
		t.Errorf("flags = %+v", o)
	}

	// 默认端口
	if d := ParseOptions(nil); d.Port != 3080 {
		t.Errorf("默认端口 = %d, want 3080", d.Port)
	}

	// -Port:3199 形式
	if p := ParseOptions([]string{"-Port:3201"}); p.Port != 3201 {
		t.Errorf("Port: 形式 = %d, want 3201", p.Port)
	}

	// -Update 不带版本号 → 升级到 latest（UpdateTo 空但已知是更新请求）
	if u := ParseOptions([]string{"-Update"}); u.UpdateTo != "" {
		t.Errorf("-Update 不应带版本号，得到 %q", u.UpdateTo)
	}
	// -UpdateTo 1.2.3
	if u := ParseOptions([]string{"-UpdateTo", "1.2.3"}); u.UpdateTo != "1.2.3" {
		t.Errorf("UpdateTo = %q, want 1.2.3", u.UpdateTo)
	}
	// 后面跟着另一个开关时不能把开关当成版本号
	if u := ParseOptions([]string{"-UpdateTo", "-Force"}); u.UpdateTo != "" {
		t.Errorf("UpdateTo 吃掉了开关：%q", u.UpdateTo)
	}

	if h := ParseOptions([]string{"-?"}); !h.ShowHelp {
		t.Errorf("-? 应触发帮助")
	}
}

func TestAtoi(t *testing.T) {
	if n, ok := Atoi("3199"); !ok || n != 3199 {
		t.Errorf("Atoi(3199) = %d,%v", n, ok)
	}
	for _, bad := range []string{"", "abc", "-1", "1.5", "70000"} {
		if _, ok := Atoi(bad); ok {
			t.Errorf("Atoi(%q) 应失败", bad)
		}
	}
}
