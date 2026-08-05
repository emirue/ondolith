package commerce

import (
	"errors"
	"testing"
)

// The properties D81 named, each as its own case. They are stated as
// "this pair is refused" rather than "the table has N entries" — a count says
// nothing about which arrow went missing.
func TestRefusedTransitions(t *testing.T) {
	cases := []struct {
		why      string
		from, to Status
		actor    Actor
		wantErr  error
	}{
		{"역전이는 없다. 실수는 새 상태(반품)로 표현한다",
			StatusShipping, StatusPreparing, "A-506", ErrTransitionNotAllowed},
		{"역전이", StatusDelivered, StatusShipping, "A-506", ErrTransitionNotAllowed},
		{"역전이", StatusPaid, StatusPaymentPending, "A-506", ErrTransitionNotAllowed},

		{"수거를 거치지 않은 환불 — 물건을 못 받고 돈만 나간다",
			StatusReturnOpen, StatusRefunded, "A-511", ErrTransitionNotAllowed},

		{"구매확정 이후에는 반품을 받지 않는다 (FR-617)",
			StatusConfirmed, StatusReturnOpen, "P-511", ErrTransitionNotAllowed},
		{"구매확정 이후에는 교환을 받지 않는다 (FR-618)",
			StatusConfirmed, StatusExchangeOpen, "P-512", ErrTransitionNotAllowed},
		{"구매확정은 최종 상태다", StatusConfirmed, StatusRefunded, "A-507", ErrTransitionNotAllowed},

		{"배송 전에는 취소이지 환불이 아니다",
			StatusPaid, StatusRefunded, "A-507", ErrTransitionNotAllowed},
		{"배송 후에는 환불이지 취소가 아니다",
			StatusDelivered, StatusCancelled, "A-507", ErrTransitionNotAllowed},

		{"결제대기에서 배송으로 건너뛸 수 없다",
			StatusPaymentPending, StatusShipping, "A-506", ErrTransitionNotAllowed},
		{"결제 없이 배송준비로 갈 수 없다",
			StatusPaymentPending, StatusPreparing, "A-506", ErrTransitionNotAllowed},

		{"교환은 차액을 건너뛰고 발송되지 않는다 — 여기는 A-511 이 판단한다",
			StatusExchangeDiffDue, StatusExchangeShipped, "A-511", ErrActorNotAllowed},
		{"구매자가 자기 주문을 환불로 보낼 수 없다 (D14 5-1)",
			StatusDelivered, StatusRefunded, "P-507", ErrActorNotAllowed},
		{"피킹 대조는 확인이지 전이가 아니다 (FR-623)",
			StatusPreparing, StatusShipping, "A-516", ErrActorNotAllowed},
		{"자동 확정은 시스템이지만 배송은 사람이 누른다",
			StatusPreparing, StatusShipping, ActorSystem, ErrActorNotAllowed},

		{"모르는 상태", "발송대기", StatusShipping, "A-506", ErrUnknownStatus},
		{"모르는 목적 상태", StatusPaid, "부분환불", "A-507", ErrUnknownStatus},
	}
	for _, c := range cases {
		err := CanTransition(c.from, c.to, c.actor)
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s → %s (%s): %v, want %v — %s",
				c.from, c.to, c.actor, err, c.wantErr, c.why)
		}
	}
}

// The arrows that must work. A machine that refuses everything passes the test
// above.
func TestAllowedTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		actor    Actor
	}{
		{StatusPaymentPending, StatusPaid, "P-408"},
		{StatusPaymentPending, StatusPaymentFailed, ActorSystem},
		{StatusPaymentPending, StatusDepositPending, ActorSystem},
		{StatusDepositPending, StatusPaid, "P-905"},
		{StatusDepositPending, StatusPaymentFailed, ActorSystem},
		{StatusPaid, StatusPreparing, "A-506"},
		{StatusPaid, StatusCancelled, "P-506"},
		{StatusPaid, StatusCancelled, "A-507"},
		{StatusPreparing, StatusShipping, "A-506"},
		{StatusPreparing, StatusCancelled, "P-506"},
		{StatusShipping, StatusDelivered, "A-506"},
		{StatusShipping, StatusRefunded, "A-507"},
		{StatusDelivered, StatusConfirmed, "P-510"},
		{StatusDelivered, StatusConfirmed, ActorSystem},
		{StatusDelivered, StatusRefunded, "A-507"},
		{StatusDelivered, StatusReturnOpen, "P-511"},
		{StatusDelivered, StatusExchangeOpen, "P-512"},
		{StatusReturnOpen, StatusReturnPickedUp, "A-511"},
		{StatusReturnOpen, StatusDelivered, "A-511"},
		{StatusReturnPickedUp, StatusRefunded, "A-511"},
		{StatusExchangeOpen, StatusExchangePicked, "A-511"},
		{StatusExchangeOpen, StatusDelivered, "A-511"},
		{StatusExchangePicked, StatusExchangeShipped, "A-511"},
		{StatusExchangePicked, StatusExchangeDiffDue, "A-511"},
		{StatusExchangeDiffDue, StatusExchangeShipped, "P-514"},
		{StatusExchangeShipped, StatusDelivered, ActorSystem},
	}
	for _, c := range cases {
		if err := CanTransition(c.from, c.to, c.actor); err != nil {
			t.Errorf("%s → %s (%s) 가 막혔다: %v", c.from, c.to, c.actor, err)
		}
	}
}

// 교환은 성공하면 배송완료로 돌아오고, 그 뒤 다시 구매확정하거나 또 반품할 수
// 있다 (D14 「교환 복귀」). 상태 하나만 보면 맞는데 왕복이 되는지는 이어서
// 걸어 봐야 드러난다.
func TestExchangeReturnsToDeliveredAndCanRunAgain(t *testing.T) {
	path := []struct {
		to    Status
		actor Actor
	}{
		{StatusExchangeOpen, "P-512"},
		{StatusExchangePicked, "A-511"},
		{StatusExchangeDiffDue, "A-511"},
		{StatusExchangeShipped, "P-514"},
		{StatusDelivered, ActorSystem},
		// 돌아온 뒤 다시 반품할 수 있다.
		{StatusReturnOpen, "P-511"},
		{StatusReturnPickedUp, "A-511"},
		{StatusRefunded, "A-511"},
	}
	cur := StatusDelivered
	for _, step := range path {
		if err := CanTransition(cur, step.to, step.actor); err != nil {
			t.Fatalf("%s → %s (%s): %v", cur, step.to, step.actor, err)
		}
		cur = step.to
	}
	if !Terminal(cur) {
		t.Errorf("%s 가 최종 상태가 아니다", cur)
	}
}

// A-506's dropdown is built from Next. If it offered every status, the server
// check would be the only defence and D14 opens by naming that the commonest
// mistake.
func TestNextIsWhatTheDropdownOffers(t *testing.T) {
	got := Next(StatusPreparing)
	want := []Status{StatusShipping, StatusCancelled} // 유니코드 순 (ㅂ < ㅊ)
	if len(got) != len(want) {
		t.Fatalf("배송준비 다음 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("배송준비 다음 = %v, want %v", got, want)
		}
	}
	if n := Next(StatusConfirmed); len(n) != 0 {
		t.Errorf("구매확정 다음 = %v, want 없음", n)
	}
	if n := Next("없는상태"); len(n) != 0 {
		t.Errorf("없는 상태 다음 = %v", n)
	}
}

// 최종 상태와 "모르는 상태" 는 다르다. 하나로 뭉치면 오타 하나가 "끝난 주문"
// 으로 조용히 처리된다.
func TestTerminalIsNotTheSameAsUnknown(t *testing.T) {
	for _, s := range []Status{StatusConfirmed, StatusCancelled, StatusRefunded, StatusPaymentFailed} {
		if !Terminal(s) || !Known(s) {
			t.Errorf("%s: Terminal=%v Known=%v, want true/true", s, Terminal(s), Known(s))
		}
	}
	for _, s := range []Status{StatusPaid, StatusDelivered, StatusExchangeDiffDue} {
		if Terminal(s) {
			t.Errorf("%s 가 최종 상태로 잡힌다", s)
		}
	}
	if Terminal("없는상태") || Known("없는상태") {
		t.Error("없는 상태가 최종 상태이거나 알려진 상태다")
	}
}

// 전이에 주체가 없으면 D14 의 "빈 라벨은 없다" 가 깨진다 — 주체 검사가 통과만
// 하는 함수가 되고, 그 사실이 어떤 전이 테스트에도 걸리지 않는다.
func TestEveryTransitionNamesAnActor(t *testing.T) {
	for from, next := range transitions {
		for to, actors := range next {
			if len(actors) == 0 {
				t.Errorf("%s → %s 에 주체가 없다", from, to)
			}
			for _, a := range actors {
				if a == "" {
					t.Errorf("%s → %s 에 빈 주체가 있다", from, to)
				}
			}
		}
	}
}
