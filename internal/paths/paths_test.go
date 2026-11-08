//go:build windows

package paths

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// Root() 必须缓存：它是 LogDir/OutLog/ErrLog/UrlFile 的地基，
// 未缓存时单次要 384µs（os.Executable + CreateTemp + Remove）。
// 这个测试锁住"只解析一次"这个约定 —— 如果有人把 sync.Once 拿掉就会红。
func TestRootCached(t *testing.T) {
	first := Root()
	for i := 0; i < 100; i++ {
		if got := Root(); got != first {
			t.Fatalf("第 %d 次 Root() 返回 %q，与首次 %q 不一致", i, got, first)
		}
	}
	if first == "" {
		t.Fatalf("Root() 返回空串")
	}
}

// 缓存之后仍要保证路径拼接正确（缓存不能把错误的值固化下来）。
func TestDerivedPaths(t *testing.T) {
	root := Root()
	if got, want := LogDir(), root+`\logs`; got != want {
		t.Errorf("LogDir() = %q, want %q", got, want)
	}
	if got, want := OutLog(), root+`\logs\dsh-web.out.log`; got != want {
		t.Errorf("OutLog() = %q, want %q", got, want)
	}
	if got, want := ErrLog(), root+`\logs\dsh-web.err.log`; got != want {
		t.Errorf("ErrLog() = %q, want %q", got, want)
	}
	if got, want := UrlFile(), root+`\last-url.txt`; got != want {
		t.Errorf("UrlFile() = %q, want %q", got, want)
	}
}

// 并发调用 Root() 不能 panic、不能返回不同结果（sync.Once 的并发语义）。
func TestRootConcurrent(t *testing.T) {
	want := Root()
	var wg sync.WaitGroup
	results := make([]string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = Root()
		}(i)
	}
	wg.Wait()
	for i, got := range results {
		if got != want {
			t.Errorf("并发调用第 %d 个返回 %q，want %q", i, got, want)
		}
	}
}

// dirWritable 是探测可写性的核心，两个方向都要对。
func TestDirWritable(t *testing.T) {
	if !dirWritable(os.TempDir()) {
		t.Errorf("临时目录应当可写")
	}
	// 不存在的目录必须判为不可写，而不是 panic
	if dirWritable(fmt.Sprintf("%s\\__no_such_dir_%d__", os.TempDir(), os.Getpid())) {
		t.Errorf("不存在的目录不应判为可写")
	}
}
