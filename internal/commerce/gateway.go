package commerce

import (
	"context"
	"errors"
)

// Gateway is the PG contract (FR-605).
//
// 메서드가 넷뿐인 것은 D50 이 넷만 필요하다고 적었기 때문이다 — 승인·취소·
// 조회·웹훅 검증. 부분 취소는 Cancel 의 인자이고, 대사는 Get 의 호출자이며,
// 결제창 호출은 브라우저 JS SDK 라 서버에 없다.
//
// 구현체가 하나(토스페이먼츠)뿐인데 인터페이스를 두는 것은 보통 과설계지만,
// 여기서는 두 번째 PG 가 오는 것이 확실하고 PG 를 코드 전반에 흩뿌린 뒤
// 걷어내는 비용이 크다 (D50 「PG 어댑터」).
//
// **이 파일에 토스페이먼츠 고유 이름이 없다.** paymentKey·orderId 같은 필드명은
// 토스의 것이지만, 여기서는 PaymentKey·OrderNo 라는 중립 이름을 쓰고 어댑터가
// 옮긴다. 고유 이름이 새어나오면 두 번째 PG 는 "토스 흉내" 를 구현하게 된다.
type Gateway interface {
	// Confirm 은 결제를 확정한다. D50 결제 흐름 6단계.
	Confirm(ctx context.Context, req ConfirmRequest) (*Payment, error)
	// Cancel 은 전액 또는 부분 취소한다. Amount 가 0 이면 전액이다.
	Cancel(ctx context.Context, req CancelRequest) (*Payment, error)
	// Get 은 상태를 조회한다. 승인 결과가 불명일 때 재승인 대신 이것을 쓴다
	// (D50 「10분 만료와 복구」: 재승인 시도 금지).
	Get(ctx context.Context, paymentKey string) (*Payment, error)
	// VerifyWebhook 은 수신 페이로드의 진위를 확인한다.
	VerifyWebhook(ctx context.Context, body []byte) (*WebhookEvent, error)
}

// PaymentStatus is the gateway-neutral outcome. payments.status 의 세 값과
// 같은 집합이다 — 어댑터가 PG 고유 상태 문자열을 여기로 접는다.
type PaymentStatus string

const (
	PaymentPending  PaymentStatus = "대기"
	PaymentApproved PaymentStatus = "승인"
	PaymentFailed   PaymentStatus = "실패"
)

// ConfirmRequest is what the caller has already verified.
//
// Amount 를 받는 이유는 PG 에 보내기 위해서만이 아니다. D50 결제 흐름 4단계의
// 금액 대조(FR-607)는 **이 구조체를 만들기 전에** 끝나 있어야 하고, 그 결과가
// 여기 실린다 — 클라이언트가 보낸 금액이 이 자리에 오면 대조가 무의미해진다.
type ConfirmRequest struct {
	PaymentKey string
	OrderNo    string
	Amount     int
	// IdempotencyKey 는 호출자가 만든다. 어댑터가 만들면 재시도할 때마다 새
	// 키가 나와서 멱등이 아니게 된다 — 재시도야말로 이 헤더가 있는 이유다
	// (D50 「멱등성」: 헤더는 PG 쪽 중복 처리를, DB 유니크는 우리 쪽 중복
	// 기록을 막는다. 서로를 대신하지 못한다).
	IdempotencyKey string
}

// CancelRequest — Amount 0 은 전액 취소다.
type CancelRequest struct {
	PaymentKey     string
	Reason         string
	Amount         int
	IdempotencyKey string
}

// Payment is what came back, in our vocabulary.
type Payment struct {
	PaymentKey string
	OrderNo    string
	Status     PaymentStatus
	// Amount 는 PG 가 확정한 금액이다. 요청 금액과 다를 수 있고, 다르면
	// 호출자가 거부한다 (FR-607).
	Amount int
	// Raw 는 응답 원문이다. D50 「10분 만료와 복구」의 "승인 성공했으나 우리 DB
	// 기록 실패" 가 가장 위험한 경우이고, 사후 대조의 유일한 근거가 이것이다.
	// 어댑터가 카드 필드를 마스킹한 뒤 넣는다 (DEC-3.7).
	Raw []byte
	// Secret 은 가상계좌 웹훅 대조용이다 (D50 「웹훅」). 토스에는 서명 헤더가
	// 없고, 공식 문서가 제시하는 검증 수단은 이 값의 대조 하나뿐이다.
	Secret string
}

// WebhookEvent is a verified inbound notification.
type WebhookEvent struct {
	// EventID 는 재전송 멱등의 키다. webhook_events (pg, event_id) 유니크가
	// 같은 값을 받는다 (FR-610).
	EventID string
	Type    string
	OrderNo string
	// PaymentKey 는 조회 API 로 실제 상태를 확인할 때 쓴다. D50 은 웹훅 본문을
	// 진실의 근거로 삼지 말라고 적었다 — secret 은 아는 사람이 흉내낼 수 있고,
	// 그 사람이 곧 우리에게 입금을 통보할 수 있다.
	PaymentKey string
	Secret     string
	Raw        []byte
}

var (
	// ErrPaymentExpired is D50's 10분 창을 넘긴 경우. 재승인하지 않고 주문을
	// 결제대기로 되돌린다.
	ErrPaymentExpired = errors.New("commerce: 결제 인증이 만료되었습니다")
	// ErrPaymentUnknown is 결과 불명이다. 재승인 시도 금지 — 조회 API 로
	// 실제 상태를 확인한 뒤 판단한다.
	ErrPaymentUnknown = errors.New("commerce: 결제 결과를 확인하지 못했습니다")
	// ErrWebhookUnverified is secret 대조 실패다. 호출자는 조용히 버린다 —
	// 무엇이 틀렸는지 알려주지 않는다.
	ErrWebhookUnverified = errors.New("commerce: 웹훅을 검증하지 못했습니다")
	// ErrAmountMismatch is FR-607.
	ErrAmountMismatch = errors.New("commerce: 요청 금액과 결제 금액이 다릅니다")
)

// VerifyAmount is D50 결제 흐름 4단계, as its own function.
//
// 함수로 떼어 둔 이유: 이 대조를 건너뛰는 것이 FR-607 이 막으려는 사고 그 자체
// 인데, 핸들러 안의 if 한 줄이면 어느 경로가 그것을 지나갔는지 셀 수 없다.
// 승인 경로와 웹훅 경로가 같은 것을 부른다 (D50 「웹훅」 마지막 항목).
func VerifyAmount(stored, received int) error {
	if stored != received {
		return ErrAmountMismatch
	}
	return nil
}
