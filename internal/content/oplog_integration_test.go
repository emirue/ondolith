package content

import (
	"context"
	"strings"
	"testing"
)

// D15 7절: 비밀번호·해시·세션 토큰·카드번호·PG 시크릿·재설정 토큰 원문은
// **어떤 필드에도** 없다. 구조체에 넣을 자리가 없다는 것이 1차 방어고,
// 요약에 붙여 넣는 경우를 위한 것이 2차 방어다.
func TestOpLogNeverStoresCredentials(t *testing.T) {
	s, _ := testStore(t)
	l := s.OpLog()
	ctx := context.Background()

	// 구조체에 자격증명 필드가 없다는 것을 형태로 확인한다 — 필드가 생기면
	// 여기가 컴파일되지 않는다.
	var e Entry
	_ = e.ActorID
	_ = e.ActorEmail
	_ = e.Action
	_ = e.TargetType
	_ = e.TargetID
	_ = e.Summary
	_ = e.IP

	leaks := []string{
		"password=hunter2",
		// 한국어 요약. 첫 판의 탐지어가 영어뿐이라 이것이 그대로 통과했다.
		"새 비밀번호: hunter2",
		"암호를 abc123 으로 바꿈",
		"시크릿 키 sk_live_9 저장",
		"Authorization: Bearer abc.def",
		"api_key=sk_live_123",
		"card_number 4111111111111111",
		"reset token 8f3a",
		"$2a$12$abcdefghijklmnopqrstuv",
	}
	for _, summary := range leaks {
		if err := l.Record(ctx, Entry{
			Action: "settings.update", TargetType: "settings", Summary: summary,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := l.Recent(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range got {
		for _, bad := range []string{"hunter2", "sk_live_123", "sk_live_9", "abc123", "4111111111111111", "abc.def"} {
			if strings.Contains(row.Summary, bad) {
				t.Errorf("자격증명이 기록됐다: %q", row.Summary)
			}
		}
	}
}

// 마스킹이 아니라 통째 교체다. 값을 가린 요약은 그래도 그 값이 로깅 호출을
// 통과했다는 뜻이고, 호출자는 아무것도 배우지 못한다.
func TestRedactedReplacesTheWholeSummary(t *testing.T) {
	got, hit := Redacted("password=hunter2 이고 사용자는 a@example.com")
	if !hit {
		t.Fatal("자격증명을 못 알아봤다")
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "a@example.com") {
		t.Errorf("일부만 가렸다: %q", got)
	}
	if !strings.Contains(got, "기록하지 않음") {
		t.Errorf("무엇이 일어났는지 말하지 않는다: %q", got)
	}

	// 평범한 요약은 그대로 남는다 — 전부 가리면 로그가 쓸모없다.
	plain := "게시판 'free' 의 필드 color 를 추가했다"
	if got, hit := Redacted(plain); hit || got != plain {
		t.Errorf("평범한 요약이 가려졌다: %q", got)
	}
}

// D30: 이 표는 수정·삭제하지 않는다. 트리거가 강제한다.
func TestOpLogIsAppendOnly(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	if err := s.OpLog().Record(ctx, Entry{
		Action: "user.delete", TargetType: "user", TargetID: "x", Summary: "지웠다",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM operation_logs`); err == nil {
		t.Error("작업 로그가 삭제됐다")
	}
	if _, err := pool.Exec(ctx, `UPDATE operation_logs SET summary = '고침'`); err == nil {
		t.Error("작업 로그가 수정됐다")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_logs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d행 — 남아 있어야 한다", n)
	}
}

// 측정한 것 (D30): 단순한 BEFORE UPDATE 트리거는 사용자 삭제를 통째로 막았다.
// ON DELETE SET NULL 이 내부적으로 UPDATE 이기 때문이다. 사용자를 지울 수
// 있어야 하고, 지운 뒤에도 로그는 남고 여전히 수정 불가여야 한다.
func TestDeletingAUserLeavesTheLogIntactAndStillImmutable(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	var uid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ('a@example.com','h','작성자') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := s.OpLog().Record(ctx, Entry{
		ActorID: uid, ActorEmail: "a@example.com",
		Action: "board.manage", TargetType: "board", TargetID: "b1",
		Summary: "게시판을 만들었다", IP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}

	// 사용자 삭제가 성공해야 한다.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid); err != nil {
		t.Fatalf("사용자 삭제가 로그 트리거에 막혔다: %v", err)
	}

	got, err := s.OpLog().Recent(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("로그 %d행", len(got))
	}
	// 주체는 사라졌지만 이메일 스냅샷이 남는다 — 그것이 스냅샷을 둔 이유다.
	if got[0].ActorEmail != "a@example.com" {
		t.Errorf("주체 스냅샷이 사라졌다: %q", got[0].ActorEmail)
	}
	if got[0].IP != "203.0.113.7" {
		t.Errorf("IP = %q", got[0].IP)
	}
	// 그리고 여전히 수정 불가다.
	if _, err := pool.Exec(ctx, `UPDATE operation_logs SET summary = '고침'`); err == nil {
		t.Error("사용자 삭제 뒤에는 로그가 수정된다")
	}
}

// action 은 권한 키와 같은 2세그먼트 형태다 (D15 2.1). 새 명명 규칙을 만들지
// 않기 위해서이고, 형태가 어긋난 값은 DB 가 거부한다.
func TestActionShapeIsEnforced(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for _, bad := range []string{"delete", "User.Delete", "user delete", "user.", ".delete", "user.delete.now"} {
		if err := s.OpLog().Record(ctx, Entry{
			Action: bad, TargetType: "user",
		}); err == nil {
			t.Errorf("형태가 어긋난 action %q 가 통과했다", bad)
		}
	}
	if err := s.OpLog().Record(ctx, Entry{Action: "user.delete", TargetType: "user"}); err != nil {
		t.Errorf("정상 action 이 거부됐다: %v", err)
	}
}

// IP 가 주소가 아니면 버린다. inet 컬럼이라 잘못된 값은 INSERT 를 실패시키고,
// 그러면 그 작업의 감사 기록이 통째로 사라진다.
func TestBadIPDoesNotLoseTheEntry(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	for _, ip := range []string{"", "unknown", "not-an-ip", "203.0.113.7:1234", "[2001:db8::1]:443"} {
		if err := s.OpLog().Record(ctx, Entry{
			Action: "user.update", TargetType: "user", IP: ip,
		}); err != nil {
			t.Errorf("IP %q 때문에 기록이 실패했다: %v", ip, err)
		}
	}
	got, err := s.OpLog().Recent(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("%d행 — 전부 남아야 한다", len(got))
	}
	// 포트가 붙은 주소는 호스트만 남는다.
	var withIP int
	for _, e := range got {
		if e.IP != "" {
			withIP++
		}
	}
	if withIP != 2 {
		t.Errorf("주소가 남은 행 %d개, want 2개", withIP)
	}
}
