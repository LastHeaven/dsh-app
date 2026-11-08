//go:build windows

package tray

import (
	"sync"
	"testing"
	"time"
)

// 这些测试只碰状态访问器，**不创建窗口、不碰互斥体、不写 Shell_NotifyIcon**，
// 所以可以在已有托盘运行的情况下安全执行，也可以在 `go test -race` 下跑。
//
// 它们锁住的是一个真实缺陷：App 的 busy/exiting/updating/url/upd/wasRunning
// 曾经被 UI 线程和各 goroutine 无同步地读写。其中 busy 最危险 ——
// 菜单、管道转发、openUIAsync 三条路径都会调 startAsync，
// 先查后置的写法会让两个 goroutine 同时通过检查、各自跑一遍 Start()。

// markBusy 必须是原子的"比较并置位"：并发调用只能有一个成功。
func TestMarkBusyExclusive(t *testing.T) {
	a := &App{}
	const n = 64
	var wg sync.WaitGroup
	var wins int64
	var mu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.markBusy() {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%d 个 goroutine 同时 markBusy，成功 %d 次，应当恰好 1 次", n, wins)
	}
	if !a.isBusy() {
		t.Fatalf("markBusy 成功后 isBusy 应为 true")
	}
	a.clearBusy()
	if a.isBusy() {
		t.Fatalf("clearBusy 之后 isBusy 应为 false")
	}
}

// beginExit 同理：退出流程只能跑一遍。
func TestBeginExitExclusive(t *testing.T) {
	a := &App{}
	var wg sync.WaitGroup
	var wins int64
	var mu sync.Mutex

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.beginExit() {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("beginExit 成功 %d 次，应当恰好 1 次", wins)
	}
	if !a.isExiting() {
		t.Fatalf("beginExit 之后 isExiting 应为 true")
	}
}

// noteNotified 必须"比较并记录"原子完成：
// 并发调用时同一个版本只应有一个调用方拿到 true（即只弹一次气泡）。
func TestNoteNotifiedOnce(t *testing.T) {
	a := &App{}
	const n = 32
	var wg sync.WaitGroup
	var approved int64
	var mu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.noteNotified("1.0.3") {
				mu.Lock()
				approved++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if approved != 1 {
		t.Fatalf("同一版本被批准提示 %d 次，应当恰好 1 次", approved)
	}
	// 换一个版本应当重新允许提示
	if !a.noteNotified("1.0.4") {
		t.Fatalf("新版本应允许提示")
	}
}

// url / upd / wasRunning 的读写与其它 goroutine 并发时不能触发 data race。
// 这个用例在 -race 下才有意义（无 -race 时只要不 panic 即可）。
func TestStateAccessorsConcurrent(t *testing.T) {
	a := &App{}
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 读者：模拟 UI 线程的定时回调
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = a.getURL()
			_ = a.getUpdateState().HasUpdate
			_ = a.getWasRunning()
			_ = a.isBusy()
			_ = a.isExiting()
		}
	}()

	// 写者：模拟 startAsync / applyUpdateAsync / updateLoop
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			a.setURL("http://127.0.0.1:3080/?token=x")
			a.setUpdateState(updateState{Latest: "1.0.3", HasUpdate: true})
			a.setWasRunning(i%2 == 0)
			if a.markBusy() {
				a.clearBusy()
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// 更新状态是值类型，取快照不能被后续写入影响。
func TestUpdateStateSnapshot(t *testing.T) {
	a := &App{}
	a.setUpdateState(updateState{Current: "0.2.2", Latest: "0.2.3", HasUpdate: true})

	got := a.getUpdateState()
	if got.Latest != "0.2.3" || !got.HasUpdate {
		t.Fatalf("快照 = %+v", got)
	}

	a.setUpdateState(updateState{Current: "0.2.3", Latest: "0.2.3", HasUpdate: false})
	if got.Latest != "0.2.3" || !got.HasUpdate {
		t.Errorf("先前取到的快照被后续写入改动了：%+v", got)
	}
}
