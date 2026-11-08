//go:build windows

package win32

import "testing"

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
		scheme, host, port, path, err := SplitHTTPURL(c.raw)
		if err != nil {
			t.Errorf("SplitHTTPURL(%q) err=%v", c.raw, err)
			continue
		}
		if host != c.host || port != c.port || path != c.path || scheme != c.scheme {
			t.Errorf("SplitHTTPURL(%q) = %s %s %d %s, want %s %s %d %s",
				c.raw, scheme, host, port, path, c.scheme, c.host, c.port, c.path)
		}
	}
	for _, bad := range []string{"", "example.com", "ftp://x/y", "https://"} {
		if _, _, _, _, err := SplitHTTPURL(bad); err == nil {
			t.Errorf("SplitHTTPURL(%q) should fail", bad)
		}
	}
}
