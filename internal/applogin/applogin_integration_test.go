package applogin

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/emirue/ondolith/internal/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func store(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("ONDOLITH_TEST_DSN")
	if dsn == "" {
		t.Skip("ONDOLITH_TEST_DSN 미설정 — 통합 테스트를 건너뜁니다 (make test-integration)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, s := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool
}

const phone = "01012345678"

// **인증번호: 6자리, 5분, 다섯 번, 한 번 쓰면 끝** (D42 8절). 다시 요청하면 앞의 번호는 버려지고 횟수도 새로 센다.
// 표에는 해시만 있다.
func TestCodeIsSixDigitsFiveMinutesFiveTries(t *testing.T) {
	s, pool := store(t)
	ctx := context.Background()
	code, err := s.IssueCode(ctx, phone)
	if err != nil || len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("IssueCode = %q, %v", code, err)
	}
	var plain int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_login_codes WHERE position(convert_to($1, 'UTF8') in code_hash) > 0`, code).Scan(&plain); err != nil || plain != 0 {
		t.Errorf("표에 인증번호 원문이 있다 (%d, %v)", plain, err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	// 한 번 쓰면 끝이다 — 횟수가 남아 있어도 같은 번호는 다시 안 된다.
	if ok, err := s.VerifyCode(ctx, phone, code); !ok || err != nil {
		t.Fatalf("맞는 번호 = %v, %v", ok, err)
	}
	if ok, _ := s.VerifyCode(ctx, phone, code); ok {
		t.Error("쓴 번호를 다시 썼다 (한 번째에 맞힌 뒤)")
	}
	code, _ = s.IssueCode(ctx, phone)
	if code == wrong {
		wrong = "222222"
	}
	// 네 번 틀리고 다섯 번째에 맞히면 된다.
	for i := 0; i < 4; i++ {
		if ok, err := s.VerifyCode(ctx, phone, wrong); ok || err != nil {
			t.Fatalf("%d번째 틀린 번호 = %v, %v", i+1, ok, err)
		}
	}
	if ok, err := s.VerifyCode(ctx, phone, code); !ok || err != nil {
		t.Fatalf("다섯 번째에 맞는 번호 = %v, %v", ok, err)
	}
	if ok, _ := s.VerifyCode(ctx, phone, code); ok {
		t.Error("쓴 번호를 다시 썼다")
	}

	// 다섯 번 틀리면 맞는 번호도 안 된다.
	code, _ = s.IssueCode(ctx, phone)
	for i := 0; i < 5; i++ {
		_, _ = s.VerifyCode(ctx, phone, wrong)
	}
	if ok, _ := s.VerifyCode(ctx, phone, code); ok {
		t.Error("다섯 번 틀린 뒤 맞는 번호가 통했다")
	}
	// 다시 요청하면 앞 번호는 버려지고 새 번호는 된다.
	old := code
	code, _ = s.IssueCode(ctx, phone)
	if old != code {
		if ok, _ := s.VerifyCode(ctx, phone, old); ok {
			t.Error("버린 번호가 통했다")
		}
	}
	if ok, _ := s.VerifyCode(ctx, phone, code); !ok {
		t.Error("다시 받은 번호가 안 된다")
	}

	// 5분이 지나면 안 된다.
	code, _ = s.IssueCode(ctx, phone)
	if _, err := pool.Exec(ctx, `UPDATE app_login_codes SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.VerifyCode(ctx, phone, code); ok {
		t.Error("지난 번호가 통했다")
	}
	// 지난 행은 다음 발급이 치운다.
	if _, err := s.IssueCode(ctx, "01099998888"); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app_login_codes WHERE phone = $1`, phone).Scan(&left)
	if left != 0 {
		t.Error("지난 인증번호가 남았다")
	}
	// 없는 번호에도 오류가 아니라 「아니다」다.
	if ok, err := s.VerifyCode(ctx, "01011112222", "123456"); ok || err != nil {
		t.Errorf("요청하지 않은 번호 = %v, %v", ok, err)
	}
}

// **동시에 보낸 추측도 다섯 번까지다** — 읽고 나서 올리면 같은 순간의 요청들이 모두 「아직 네 번」을 본다.
func TestConcurrentGuessesStillStopAtFive(t *testing.T) {
	s, pool := store(t)
	ctx := context.Background()
	code, err := s.IssueCode(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.VerifyCode(ctx, phone, wrong) }()
	}
	wg.Wait()
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM app_login_codes WHERE phone = $1`, phone).Scan(&attempts); err != nil || attempts != MaxAttempts {
		t.Errorf("틀린 횟수 = %d (%v), want %d — 그 이상은 세지도 비교하지도 않는다", attempts, err, MaxAttempts)
	}
}

// **앱 토큰: 유휴 180일, 해시로만, 대상마다 끊을 수 있다** (D42 8절). 그만둔 사람은 사이트가 대상을 끊는다.
func TestSessionsAreHashedAndEndBySubject(t *testing.T) {
	s, pool := store(t)
	ctx := context.Background()
	tok, exp, err := s.StartSession(ctx, "worker-7")
	if err != nil || len(tok) != 43 {
		t.Fatalf("StartSession = %q, %v", tok, err)
	}
	var days float64
	if err := pool.QueryRow(ctx, `SELECT extract(epoch from ($1::timestamptz - now())) / 86400`, exp).Scan(&days); err != nil || days < 179.9 || days > 180.1 {
		t.Errorf("만료까지 %.2f일, want 180", days)
	}
	var plain int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app_sessions WHERE position(convert_to($1, 'UTF8') in token_hash) > 0`, tok).Scan(&plain)
	if plain != 0 {
		t.Error("표에 토큰 원문이 있다")
	}
	if sub, ok, err := s.LookupSession(ctx, tok); !ok || err != nil || sub != "worker-7" {
		t.Fatalf("LookupSession = %q, %v, %v", sub, ok, err)
	}
	for _, bad := range []string{"", tok + "x", tok[:42], strings.ToUpper(tok)} {
		if bad == tok {
			continue
		}
		if _, ok, err := s.LookupSession(ctx, bad); ok || err != nil {
			t.Errorf("LookupSession(틀린 토큰) = %v, %v", ok, err)
		}
	}
	var used int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app_sessions WHERE last_used_at IS NOT NULL`).Scan(&used)
	if used != 1 {
		t.Error("쓴 토큰에 마지막 사용 시각이 없다")
	}

	other, _, _ := s.StartSession(ctx, "worker-7")
	keep, _, _ := s.StartSession(ctx, "worker-8")
	if err := s.EndSession(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupSession(ctx, other); ok {
		t.Error("로그아웃한 토큰이 통했다")
	}
	n, err := s.EndSubject(ctx, "worker-7")
	if err != nil || n != 1 {
		t.Fatalf("EndSubject = %d, %v", n, err)
	}
	if _, ok, _ := s.LookupSession(ctx, tok); ok {
		t.Error("끊은 대상의 토큰이 통했다")
	}
	if _, ok, _ := s.LookupSession(ctx, keep); !ok {
		t.Error("다른 대상의 토큰까지 끊었다")
	}
	// 만료가 지나면 안 되고, 다음 발급이 치운다.
	if _, err := pool.Exec(ctx, `UPDATE app_sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupSession(ctx, keep); ok {
		t.Error("지난 토큰이 통했다")
	}
	if _, _, err := s.StartSession(ctx, "worker-9"); err != nil {
		t.Fatal(err)
	}
	var total int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app_sessions`).Scan(&total)
	if total != 1 {
		t.Errorf("지난 토큰이 남았다 — 행 %d개", total)
	}
	if _, _, err := s.StartSession(ctx, ""); err == nil {
		t.Error("빈 대상으로 토큰을 만들었다")
	}
}

// **쓰는 동안 연장된다** (D42 8절) — 살아 있는 토큰을 쓰면 만료가 「지금 + 180일」이 된다. 쓰기는 지금처럼 1분에 한
// 번이다. 끝난 토큰은 늘리지 않는다 — 조회가 「살아 있다」를 본 직후 끝났어도 고치기 문장이 그 행을 건드리지 않는다.
func TestSessionsSlideWhileUsed(t *testing.T) {
	s, pool := store(t)
	ctx := context.Background()
	tok, _, err := s.StartSession(ctx, "worker-7")
	if err != nil {
		t.Fatal(err)
	}
	set := func(assign string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE app_sessions SET `+assign+` WHERE token_hash = $1`, sum(tok)); err != nil {
			t.Fatal(err)
		}
	}
	daysLeft := func() float64 {
		t.Helper()
		var d float64
		if err := pool.QueryRow(ctx, `SELECT extract(epoch from (expires_at - now())) / 86400 FROM app_sessions WHERE token_hash = $1`, sum(tok)).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	live := func() bool {
		t.Helper()
		_, ok, err := s.LookupSession(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// 하루 남은 토큰(지난 판이 만든 30일 토큰도 같다)을 2분 만에 쓰면 180일로 늘어난다.
	set(`created_at = now() - interval '29 days', expires_at = now() + interval '1 day', last_used_at = now() - interval '2 minutes'`)
	if !live() {
		t.Fatal("살아 있는 토큰이 안 통했다")
	}
	if d := daysLeft(); d < 179.9 || d > 180.1 {
		t.Errorf("쓴 뒤 만료까지 %.2f일, want 180", d)
	}

	// 1분 안에 다시 쓰면 고치지 않는다 — 요청마다 쓰기를 하나 더하지 않는다.
	set(`expires_at = now() + interval '1 day'`)
	if !live() {
		t.Fatal("살아 있는 토큰이 안 통했다")
	}
	if d := daysLeft(); d > 1.1 {
		t.Errorf("1분 안의 두 번째 사용이 만료를 고쳤다 — %.2f일", d)
	}

	// 끝난 토큰은 되살리지 않는다. 조회는 끝난 토큰을 이미 거르므로, 조회를 지난 직후 끝난 경우(그 사이 지난 토큰
	// 치우기와 겹친 경우도)는 고치기 문장을 바로 불러 흉내 낸다 — 그 WHERE 를 빼면 여기가 빨강이다.
	set(`expires_at = now() - interval '1 second', last_used_at = now() - interval '2 minutes'`)
	if live() {
		t.Error("끝난 토큰이 통했다")
	}
	if err := s.q.TouchSession(ctx, sum(tok)); err != nil {
		t.Fatal(err)
	}
	if d := daysLeft(); d > 0 {
		t.Errorf("끝난 토큰이 되살아났다 — 만료까지 %.2f일", d)
	}
}

// **맞는 번호로 동시에 확인해도 하나만 통한다** — 한 번만 쓰는 번호다. 맞힌 뒤 「번호로」 지우면 같은 순간의 요청
// 둘이 모두 통과해 토큰이 둘 생기고, 그 사이 새로 받은 번호까지 지운다. 지우기는 그 해시가 남아 있을 때만 성공한다.
func TestConcurrentRightGuessesWinOnce(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		code, err := s.IssueCode(ctx, phone)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ok, _ := s.VerifyCode(ctx, phone, code); ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("%d번째: 맞는 번호로 동시에 %d번 통했다, want 1", round+1, wins)
		}
	}
}
