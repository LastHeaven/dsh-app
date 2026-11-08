//go:build windows

// Package updater 是 dsh 版本检测与两阶段升级。改之前先读 docs/UPDATE.md。
package updater

import "strings"

// SemVer 解析与比较。
//
// 自己实现，不引第三方包（本机 go get 不可用）。按 semver.org 2.0.0：
//
//	主.次.修订 [-预发布] [+构建]
//
// 预发布比较规则：
//   - 有预发布 < 无预发布（1.0.0-rc.1 < 1.0.0）
//   - 同长度逐段比，数字段按数值（rc.10 > rc.9，**不是字典序**）
//   - 数字段 < 字母段（1.0.0-alpha.1 < 1.0.0-alpha.beta）
//   - 段少的小（1.0.0-alpha < 1.0.0-alpha.1）
type SemVer struct {
	major, minor, patch int
	pre                 []string // 预发布标识符，空表示正式版
	valid               bool
}

// ParseSemVer 解析版本号；非法版本的 Valid() 为 false。
func ParseSemVer(s string) SemVer {
	var v SemVer
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return v
	}
	// 丢掉 +build
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	// 拆预发布
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}

	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return v
	}
	nums := []int{0, 0, 0}
	for i, p := range parts {
		n, ok := atoiStrict(p)
		if !ok {
			return v
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	if pre != "" {
		v.pre = strings.Split(pre, ".")
	}
	v.valid = true
	return v
}

// Valid 表示版本号是否解析成功。
func (v SemVer) Valid() bool { return v.valid }

// atoiStrict 只接受纯数字，不接受空串、符号、空白。
func atoiStrict(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, true
}

// CompareSemVer 返回 -1 / 0 / 1。
// 任一版本非法时返回 0（调用方应先检查 Valid，避免把垃圾当成"相同"）。
func CompareSemVer(a, b SemVer) int {
	if !a.valid || !b.valid {
		return 0
	}
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}

	// 正式版 > 预发布版
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}

	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xok := atoiStrict(x)
		yn, yok := atoiStrict(y)
		switch {
		case xok && yok:
			if c := cmpInt(xn, yn); c != 0 {
				return c
			}
		case xok && !yok:
			return -1 // 数字段 < 字母段
		case !xok && yok:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	// 前缀相同：段多的更大
	return cmpInt(len(a.pre), len(b.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// IsNewer 判断 remote 是否比 local 新。任一非法一律返回 false ——
// 宁可漏报也绝不能提示一个不存在的更新。
func IsNewer(remote, local string) bool {
	r := ParseSemVer(remote)
	l := ParseSemVer(local)
	if !r.valid || !l.valid {
		return false
	}
	return CompareSemVer(r, l) > 0
}
