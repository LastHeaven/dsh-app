//go:build windows

package updater

import "strings"

// 手写的极简 JSON 取值。
//
// **不引 encoding/json**：实测它让二进制多 264 KB，而我们只需要从一个
// 8 KB 的 package.json 里抠一个字段，或者解析 67 字节的 dist-tags。

// JSONTopString 取顶层对象里某个键的字符串值。
//
// 只认深度 1 的键 —— package.json 的 dependencies 里也可能出现
// "@deepseek-ai/dsh" 这样的同名键，不限制深度会取错值。
func JSONTopString(s, key string) string {
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

// JSONMapString 解析扁平的 {"a":"b",...}，够 dist-tags 用。
func JSONMapString(s string) map[string]string {
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
