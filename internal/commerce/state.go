// Package commerce holds the rules that are true regardless of which screen or
// which PG asked. The state machine is here rather than in a handler because
// A-506, P-506, P-510, P-511, P-512, P-514 and the webhook all move the same
// order, and a rule that lives in one of them is not a rule.
package commerce

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Status is an order's state. The values are D14 5절's labels verbatim —
// returns and shipments reuse them (D30) rather than inventing a second
// vocabulary for the same concept.
type Status string

const (
	StatusPaymentPending  Status = "결제대기"
	StatusDepositPending  Status = "입금대기"
	StatusPaid            Status = "결제완료"
	StatusPaymentFailed   Status = "결제실패"
	StatusPreparing       Status = "배송준비"
	StatusShipping        Status = "배송중"
	StatusDelivered       Status = "배송완료"
	StatusConfirmed       Status = "구매확정"
	StatusCancelled       Status = "취소"
	StatusRefunded        Status = "환불"
	StatusReturnOpen      Status = "반품접수"
	StatusReturnPickedUp  Status = "반품수거"
	StatusExchangeOpen    Status = "교환접수"
	StatusExchangePicked  Status = "교환수거"
	StatusExchangeDiffDue Status = "차액결제대기"
	StatusExchangeShipped Status = "교환발송"
)

// Actor is who is allowed to make a transition — a screen ID, or the system.
//
// It is part of the table, not a comment, because D14 says "각 전이에 주체가
// 적혀 있다" and an unenforced label drifts. A-506's dropdown listing every
// status and the server accepting whatever comes back is the failure this
// package exists to prevent.
type Actor string

const (
	ActorSystem Actor = "(시스템)"
)

var (
	// ErrUnknownStatus is a value that is not in the machine at all.
	ErrUnknownStatus = errors.New("commerce: 알 수 없는 주문 상태")
	// ErrTransitionNotAllowed is a pair that is not in the table.
	ErrTransitionNotAllowed = errors.New("commerce: 허용되지 않은 상태 전이")
	// ErrActorNotAllowed is a pair that exists but not for this caller.
	ErrActorNotAllowed = errors.New("commerce: 이 화면이 일으킬 수 없는 상태 전이")
)

// transitions is D14 5절's diagram, one entry per arrow.
//
// Written as data, not as a switch: a switch lets a branch quietly cover two
// arrows, and the "표에 없는 전이가 거부된다" property then has nothing to
// compare against. This map IS the table, and the tests read it.
var transitions = map[Status]map[Status][]Actor{
	StatusPaymentPending: {
		StatusPaid:           {"P-408"},
		StatusPaymentFailed:  {ActorSystem}, // 10분 만료
		StatusDepositPending: {ActorSystem}, // 가상계좌 발급
	},
	StatusDepositPending: {
		StatusPaid:          {"P-905"},     // 웹훅 입금 확인
		StatusPaymentFailed: {ActorSystem}, // 입금 기한 초과
	},
	StatusPaid: {
		StatusPreparing: {"A-506"},
		StatusCancelled: {"P-506", "A-507"},
	},
	StatusPreparing: {
		StatusShipping:  {"A-506"},
		StatusCancelled: {"P-506", "A-507"},
	},
	StatusShipping: {
		StatusDelivered: {"A-506"},
		// 배송 후는 환불이다. 구매자가 자기 주문을 여기로 보낼 수 없어서
		// A-507 뿐이다 (D14 5-1: P-507 은 요청 행만 만든다).
		StatusRefunded: {"A-507"},
	},
	StatusDelivered: {
		StatusConfirmed:    {"P-510", ActorSystem}, // 자동 확정 (A-512 기본 8일)
		StatusRefunded:     {"A-507"},
		StatusReturnOpen:   {"P-511"},
		StatusExchangeOpen: {"P-512"},
	},
	StatusReturnOpen: {
		StatusReturnPickedUp: {"A-511"},
		StatusDelivered:      {"A-511"}, // 거부
	},
	StatusReturnPickedUp: {
		// 수거 확인 후에만. 반품접수에서 곧바로 환불로 가는 화살표는 없다 —
		// 물건을 못 받고 돈만 나가는 것을 상태로 막는다.
		StatusRefunded: {"A-511"},
	},
	StatusExchangeOpen: {
		StatusExchangePicked: {"A-511"},
		StatusDelivered:      {"A-511"}, // 거부 (재고 예약 해제)
	},
	StatusExchangePicked: {
		StatusExchangeShipped: {"A-511"}, // 차액 ≤ 0
		StatusExchangeDiffDue: {"A-511"}, // 차액 > 0
	},
	StatusExchangeDiffDue: {
		StatusExchangeShipped: {"P-514"},
	},
	StatusExchangeShipped: {
		StatusDelivered: {ActorSystem}, // 재배송 도착
	},

	// 최종 상태. 나가는 화살표가 없다는 사실이 데이터로 있어야 "알 수 없는
	// 상태" 와 "끝난 상태" 가 구분된다.
	StatusPaymentFailed: {},
	StatusConfirmed:     {},
	StatusCancelled:     {},
	StatusRefunded:      {},
}

// Known reports whether s is a status the machine knows.
func Known(s Status) bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether nothing leaves s.
func Terminal(s Status) bool {
	next, ok := transitions[s]
	return ok && len(next) == 0
}

// Next lists the states `actor` can move s to, sorted, for a screen's dropdown.
//
// The dropdown is built from this rather than from the full status list. That
// is the whole point: if the screen offers only what the table allows, the
// server check below stops being the first line of defence and becomes the
// second one.
//
// **행위자를 함께 본다.** 전이표는 쌍마다 그것을 일으킬 수 있는 화면을 적어
// 두는데, 이 함수가 그 목록을 버리고 쌍만 내면 화면은 **자기가 일으킬 수 없는
// 전이를 제시한다.** A-506 이 실제로 그랬다: 결제대기 주문의 유일한 선택지가
// 「결제실패」였고 — 그것은 P-409 결제 실패 콜백만 일으킬 수 있다 — 고를 때마다
// 422 가 났다. 고를 수 있는 것이 전부 실패하는 화면은 아무것도 못 하는 화면이다.
func Next(s Status, actor Actor) []Status {
	out := make([]Status, 0, len(transitions[s]))
	for to, actors := range transitions[s] {
		if slices.Contains(actors, actor) {
			out = append(out, to)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CanTransition checks one move.
//
// Both the pair and the actor are checked. Checking only the pair would let
// P-506 (a buyer) drive 배송완료 → 환불, which D14 5-1 refuses on the grounds
// that money going out after delivery cannot be undone without approval.
func CanTransition(from, to Status, actor Actor) error {
	next, ok := transitions[from]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, from)
	}
	if !Known(to) {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, to)
	}
	actors, ok := next[to]
	if !ok {
		return fmt.Errorf("%w: %s → %s", ErrTransitionNotAllowed, from, to)
	}
	for _, a := range actors {
		if a == actor {
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s (%s)", ErrActorNotAllowed, from, to, actor)
}
