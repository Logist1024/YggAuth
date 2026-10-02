// Package clock 抽象「时间」。
//
// 平台层能力,与业务无关。核心价值在于可注入:会话过期、令牌有效期、
// 邮件冷却这类逻辑全部依赖时间,直接调 time.Now 会让测试只能 sleep 等待。
// 用 Clock 接口后,测试可以把时间拨到任意时刻,断言立刻返回。
package clock

import (
	"time"
)

// Clock 是时间抽象。
type Clock interface {
	// Now 返回当前时刻。
	Now() time.Time
	// Since 返回经过时长。
	Since(t time.Time) time.Duration
	// NewTimer 返回一个定时器。
	NewTimer(d time.Duration) Timer
	// Sleep 阻塞一段时间。
	Sleep(d time.Duration)
	// After 返回一个 channel,d 时间后收到一次当前时刻。
	After(d time.Duration) <-chan time.Time
}

// Timer 是定时器抽象。
type Timer interface {
	// C 返回时间到达的 channel。
	C() <-chan time.Time
	// Stop 停止定时器,返回是否成功停止。
	Stop() bool
	// Reset 重置定时器。
	Reset(d time.Duration) bool
}

// Real 是真实时钟。
type Real struct{}

// New 返回真实时钟。
func New() Clock { return Real{} }

// Now 返回当前 UTC 时刻。
func (Real) Now() time.Time { return time.Now().UTC() }

// Since 返回经过时长。
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }

// Sleep 阻塞一段时间。
func (Real) Sleep(d time.Duration) { time.Sleep(d) }

// After 返回一个在 d 之后收到当前时刻的 channel。
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// NewTimer 返回真实定时器。
func (Real) NewTimer(d time.Duration) Timer { return realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

// C 返回时间到达的 channel。
func (r realTimer) C() <-chan time.Time { return r.t.C }

// Stop 停止定时器。
func (r realTimer) Stop() bool { return r.t.Stop() }

// Reset 重置定时器。
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// Mock 是可控时钟,供测试使用。
//
// 并发安全:后台可能同时有定时器 goroutine 与测试代码推进时间。
type Mock struct {
	mu       chan struct{}
	now      time.Time
	timers   []*mockTimer
	sleepers chan time.Time
}

// NewMock 创建一个起始于指定时刻的模拟时钟。
func NewMock(start time.Time) *Mock {
	return &Mock{
		mu:       make(chan struct{}, 1),
		now:      start,
		sleepers: make(chan time.Time, 64),
	}
}

func (m *Mock) lock()   { m.mu <- struct{}{} }
func (m *Mock) unlock() { <-m.mu }

// Now 返回模拟时刻。
func (m *Mock) Now() time.Time {
	m.lock()
	defer m.unlock()
	return m.now
}

// Since 返回经过时长。
func (m *Mock) Since(t time.Time) time.Duration { return m.Now().Sub(t) }

// Sleep 在模拟时钟下直接返回:模拟时钟不会真的等待。
func (m *Mock) Sleep(time.Duration) {}

// Advance 把模拟时间往前拨,并唤醒到期的定时器与 After。
func (m *Mock) Advance(d time.Duration) {
	m.lock()
	m.now = m.now.Add(d)
	now := m.now

	var fire []*mockTimer
	kept := m.timers[:0]
	for _, t := range m.timers {
		switch {
		case t.stopped || t.fired:
			// 已停止或已触发的定时器不再参与后续调度
		case !t.deadline.After(now):
			t.fired = true
			fire = append(fire, t)
		default:
			kept = append(kept, t)
		}
	}
	m.timers = kept
	m.unlock()

	for _, t := range fire {
		// 缓冲区容量为 1,重复触发不会阻塞
		t.ch <- now
	}

	// 唤醒等待中的 After 调用方
	for i := 0; i < cap(m.sleepers); i++ {
		select {
		case m.sleepers <- now:
		default:
			return
		}
	}
}

// Set 把模拟时间设置为指定时刻(只允许向前拨,避免时间倒流造成断言混乱)。
func (m *Mock) Set(t time.Time) {
	m.lock()
	if t.After(m.now) {
		m.now = t
	}
	m.unlock()
	m.Advance(0)
}

// NewTimer 返回模拟定时器。
func (m *Mock) NewTimer(d time.Duration) Timer {
	t := &mockTimer{
		clock:    m,
		ch:       make(chan time.Time, 1),
		deadline: m.Now().Add(d),
	}
	m.lock()
	m.timers = append(m.timers, t)
	m.unlock()
	return t
}

// After 返回一个在 d 之后收到模拟时刻的 channel。
func (m *Mock) After(d time.Duration) <-chan time.Time {
	out := make(chan time.Time, 1)
	deadline := m.Now().Add(d)
	go func() {
		for {
			now := m.Now()
			if !now.Before(deadline) {
				out <- now
				return
			}
			<-m.sleepers
		}
	}()
	return out
}

type mockTimer struct {
	clock    *Mock
	ch       chan time.Time
	deadline time.Time
	fired    bool
	stopped  bool
}

// C 返回时间到达的 channel。
func (t *mockTimer) C() <-chan time.Time { return t.ch }

// Stop 停止定时器。
func (t *mockTimer) Stop() bool {
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

// Reset 重置定时器,重新进入调度。
func (t *mockTimer) Reset(d time.Duration) bool {
	active := !t.stopped
	t.stopped = false
	t.fired = false
	t.deadline = t.clock.Now().Add(d)

	t.clock.lock()
	t.clock.timers = append(t.clock.timers, t)
	t.clock.unlock()
	return active
}

// 确保 Mock 满足接口。
var (
	_ Clock = Real{}
	_ Clock = (*Mock)(nil)
	_ Timer = realTimer{}
	_ Timer = (*mockTimer)(nil)
)
