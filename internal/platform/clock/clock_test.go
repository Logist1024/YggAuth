package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yggauth/yggauth/internal/platform/clock"
)

// 会话滑动过期、令牌有效期这类逻辑必须可测,否则只能靠 sleep 等待。
// 这里验证 Mock 确实能让时间「瞬间」前进。
func TestMockAdvancesInstantly(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewMock(start)

	require.Equal(t, start, c.Now())
	require.Equal(t, time.Duration(0), c.Since(start))

	c.Advance(90 * time.Minute)
	require.Equal(t, start.Add(90*time.Minute), c.Now())
	require.Equal(t, 90*time.Minute, c.Since(start))
}

// 模拟时钟下 Sleep 不真的阻塞,否则测试会真的睡满时长。
func TestMockSleepDoesNotBlock(t *testing.T) {
	c := clock.NewMock(time.Now())

	done := make(chan struct{})
	go func() {
		c.Sleep(10 * time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Mock.Sleep 不应真的阻塞")
	}
}

func TestMockTimerFiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewMock(start)

	timer := c.NewTimer(time.Minute)
	select {
	case <-timer.C():
		t.Fatal("时间未到不应触发")
	default:
	}

	c.Advance(2 * time.Minute)

	select {
	case at := <-timer.C():
		require.Equal(t, start.Add(2*time.Minute), at)
	case <-time.After(time.Second):
		t.Fatal("时间到后定时器未触发")
	}
}

func TestMockStopPreventsFiring(t *testing.T) {
	c := clock.NewMock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	timer := c.NewTimer(time.Minute)
	require.True(t, timer.Stop())

	c.Advance(10 * time.Minute)
	select {
	case <-timer.C():
		t.Fatal("已停止的定时器不应触发")
	default:
	}
}

func TestRealClockMonotonic(t *testing.T) {
	c := clock.New()

	before := c.Now()
	time.Sleep(2 * time.Millisecond)
	after := c.Now()

	require.True(t, after.After(before) || after.Equal(before))
	require.Positive(t, c.Since(before))
}

// Mock 只允许向前拨,避免时间倒流造成断言混乱。
func TestMockSetNeverMovesBackward(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewMock(start)

	c.Set(start.Add(time.Hour))
	require.Equal(t, start.Add(time.Hour), c.Now())

	c.Set(start)
	require.Equal(t, start.Add(time.Hour), c.Now(), "不允许把时间往回拨")
}
