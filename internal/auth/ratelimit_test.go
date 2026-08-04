package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// clock is a fake so the recovery test does not sleep. A rate-limit test built
// on real time is slow when it passes and flaky when the machine is loaded.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestBurstThenRefuse(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 3, Window: time.Minute}

	for i := 0; i < 3; i++ {
		if !l.Allow("k", lim) {
			t.Fatalf("%d 번째 요청이 거부됐다 — burst 안이다", i+1)
		}
	}
	if l.Allow("k", lim) {
		t.Error("burst 를 넘겼는데 통과했다")
	}
}

func TestRecoversWithTime(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 3, Window: time.Minute}

	for i := 0; i < 3; i++ {
		l.Allow("k", lim)
	}
	if l.Allow("k", lim) {
		t.Fatal("소진 직후 통과했다")
	}

	// One token is worth Window/Burst = 20s.
	c.advance(19 * time.Second)
	if l.Allow("k", lim) {
		t.Error("토큰 하나가 차기 전에 통과했다")
	}
	c.advance(2 * time.Second)
	if !l.Allow("k", lim) {
		t.Error("토큰이 찼는데 거부됐다")
	}
}

// Continuous refill, not stepped windows: a stepped window lets a caller spend
// a full burst just before the boundary and another just after — double the
// intended rate at exactly the moment an attacker would aim for.
func TestRefillIsContinuousNotStepped(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 4, Window: time.Minute}

	for i := 0; i < 4; i++ {
		l.Allow("k", lim)
	}
	c.advance(time.Minute) // a full window
	got := 0
	for i := 0; i < 10; i++ {
		if l.Allow("k", lim) {
			got++
		}
	}
	if got != 4 {
		t.Errorf("한 창 뒤 허용 %d건, want 4 — 상한을 넘어 채워졌다", got)
	}
}

// Idle time must not accumulate beyond Burst. Without the cap a key left alone
// for hours banks hours' worth of tokens, so waiting becomes the way to buy an
// unbounded burst — the opposite of what the limit is for.
func TestIdleDoesNotBankUnlimitedTokens(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 3, Window: time.Minute}

	l.Allow("k", lim) // create the bucket, then leave it alone
	c.advance(24 * time.Hour)

	got := 0
	for i := 0; i < 100; i++ {
		if l.Allow("k", lim) {
			got++
		}
	}
	if got != 3 {
		t.Errorf("하루를 쉰 뒤 연속 허용 %d건, want 3 — 상한 없이 쌓였다", got)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 1, Window: time.Minute}

	if !l.Allow("a", lim) || !l.Allow("b", lim) {
		t.Fatal("서로 다른 키가 서로를 소진시켰다")
	}
	if l.Allow("a", lim) {
		t.Error("같은 키가 두 번 통과했다")
	}
}

// An unconfigured limit must deny. Reading a zero value as "unlimited" is the
// direction that fails open, and a zero value is exactly what a forgotten
// config field looks like.
func TestZeroLimitDenies(t *testing.T) {
	l := NewLimiterAt(newClock().now)
	if l.Allow("k", Limit{}) {
		t.Error("설정되지 않은 임계값이 통과시켰다")
	}
	if l.Allow("k", Limit{Burst: 5}) {
		t.Error("Window 가 0인데 통과시켰다")
	}
	if l.Allow("k", Limit{Window: time.Minute}) {
		t.Error("Burst 가 0인데 통과시켰다")
	}
}

// After a successful login the previous failures must not keep throttling the
// person who simply mistyped.
func TestForgetClearsBucket(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 1, Window: time.Minute}

	l.Allow("k", lim)
	if l.Allow("k", lim) {
		t.Fatal("소진되지 않았다")
	}
	l.Forget("k")
	if !l.Allow("k", lim) {
		t.Error("Forget 후에도 거부됐다")
	}
}

// Without a sweep the map grows once per distinct IP forever — a slow leak on
// any public site.
func TestSweepDropsIdleBuckets(t *testing.T) {
	c := newClock()
	l := NewLimiterAt(c.now)
	lim := Limit{Burst: 2, Window: time.Minute}

	l.Allow("old", lim)
	c.advance(2 * time.Hour)
	l.Allow("fresh", lim)

	if n := l.Sweep(time.Hour); n != 1 {
		t.Errorf("정리된 버킷 %d개, want 1", n)
	}
	// The fresh key keeps its state; the old one starts over.
	if !l.Allow("fresh", lim) {
		t.Error("최근 키가 정리됐다")
	}
}

// D15 4.3-2 must be transcribed exactly: these are the numbers the document
// justifies one by one, and a typo here is a security setting nobody re-reads.
func TestDefaultLimitsMatchDoc(t *testing.T) {
	d := DefaultLimits()
	want := map[string]Limit{
		"로그인 IP":     {10, time.Minute},
		"로그인 계정":     {5, time.Minute},
		"재설정 IP":     {3, time.Hour},
		"인증메일 계정":    {3, time.Hour},
		"가입 IP":      {5, time.Hour},
		"비회원주문 IP":   {5, time.Minute},
		"비회원주문 주문번호": {3, time.Hour},
		"관리자 IP":     {60, time.Minute},
		"재인증 계정":     {5, time.Minute},
		"메일테스트 계정":   {5, time.Hour},
	}
	got := map[string]Limit{
		"로그인 IP": d.LoginPerIP, "로그인 계정": d.LoginPerAccount,
		"재설정 IP": d.PasswordResetIP, "인증메일 계정": d.VerifyMailAccount,
		"가입 IP": d.SignupPerIP, "비회원주문 IP": d.GuestOrderIP,
		"비회원주문 주문번호": d.GuestOrderNo, "관리자 IP": d.AdminTreeIP,
		"재인증 계정": d.ReauthAccount, "메일테스트 계정": d.MailTestAccount,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v (D15 4.3-2)", k, got[k], w)
		}
	}
}

// -race regression: the limiter is shared by every request goroutine.
func TestConcurrentAllowIsRaceFree(t *testing.T) {
	l := NewLimiter()
	lim := Limit{Burst: 1000, Window: time.Minute}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				l.Allow(fmt.Sprintf("k%d", i%5), lim)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			l.Sweep(time.Hour)
		}
	}()
	wg.Wait()
}
