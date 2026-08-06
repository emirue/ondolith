package commerce

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Toss is the first Gateway implementation. 사양의 단일 출처는 D50 「검증된 연동
// 사양」이고, 그 표는 공식 문서로 대조한 것이다 (2026-08-05). SDK 를 쓰지 않고
// REST 로 직접 호출한다 — 표가 필요한 전부이고, 샘플 저장소의 유지 상태에
// 의존할 이유가 없다.
type Toss struct {
	secret  string
	baseURL string
	client  *http.Client
}

// String·GoString 이 시크릿을 가린다.
//
// 필드를 소문자로 두는 것만으로는 새는 것을 막지 못한다 — fmt 는 리플렉션으로
// 미노출 필드까지 찍는다. `%v` 하나가 로그에 키를 남기고, 그 로그는 지워지지
// 않는다. 테스트가 실제로 그것을 잡았다.
//
// 필드 타입에 Stringer 를 붙이는 방법은 통하지 않는다: fmt 는 미노출 필드에
// 대해 CanInterface() 가 false 라 그 메서드를 부르지 못한다. 가리는 곳은
// 구조체 자신이어야 한다.
func (t *Toss) String() string { return "commerce.Toss{" + t.baseURL + ", secret: [가림]}" }

// GoString 은 %#v 용이다. String 만 두면 %#v 가 여전히 필드를 펼친다.
func (t *Toss) GoString() string { return t.String() }

// tossMaxBody caps what we read from the gateway. 응답 원문을 통째로 보관하므로
// (D50 「10분 만료와 복구」) 상한이 없으면 그쪽이 보내는 만큼 우리 메모리와
// payments.raw_response 가 커진다.
const tossMaxBody = 1 << 20 // 1 MiB

// NewToss builds the adapter. timeout 이 인자인 이유는 D50 의 10분 창 때문이다 —
// 승인 호출이 그 창 안에서 끝나야 하고, 무한정 기다리는 클라이언트는 창을
// 넘긴 뒤에도 매달려 있다.
func NewToss(secret, baseURL string, timeout time.Duration) *Toss {
	return &Toss{
		secret:  secret,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// authHeader is `Basic base64(시크릿키 + ":")`.
//
// **콜론을 빠뜨리면 인증이 실패한다.** base64 대상은 `시크릿키:` 이지
// `시크릿키` 가 아니다 — 공식 문서도 이것을 따로 경고한다 (D50).
func (t *Toss) authHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(t.secret+":"))
}

func (t *Toss) Confirm(ctx context.Context, req ConfirmRequest) (*Payment, error) {
	body := map[string]any{
		"paymentKey": req.PaymentKey,
		"orderId":    req.OrderNo,
		"amount":     req.Amount,
	}
	return t.post(ctx, "/v1/payments/confirm", body, req.IdempotencyKey)
}

func (t *Toss) Cancel(ctx context.Context, req CancelRequest) (*Payment, error) {
	body := map[string]any{"cancelReason": req.Reason}
	// 생략하면 전액 취소다 (D50). 0 을 보내면 "0원 취소" 라는 다른 뜻이 된다.
	if req.Amount > 0 {
		body["cancelAmount"] = req.Amount
	}
	// paymentKey 가 경로에 들어간다. PG 가 준 값이지만 그대로 잇지 않고
	// 이스케이프한다 — 값의 출처를 신뢰하는 것과 형태를 신뢰하는 것은 다르고,
	// `../` 하나가 다른 엔드포인트를 부른다.
	path := "/v1/payments/" + url.PathEscape(req.PaymentKey) + "/cancel"
	return t.post(ctx, path, body, req.IdempotencyKey)
}

func (t *Toss) Get(ctx context.Context, paymentKey string) (*Payment, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		t.baseURL+"/v1/payments/"+url.PathEscape(paymentKey), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", t.authHeader())
	return t.do(httpReq)
}

// VerifyWebhook parses the body. 서명 헤더가 없으므로 (D50, 2026-08-05 확인)
// 여기서 하는 것은 형태 검증까지다.
//
// secret 대조는 호출자가 한다 — 대조 상대인 승인 응답의 secret 은 payments
// 행에 있고, 어댑터는 DB 를 모른다. 그리고 D50 이 적었듯 이 값은 아는 사람이
// 흉내낼 수 있으므로, 웹훅은 신호로만 쓰고 실제 상태는 Get 으로 확인한다.
func (t *Toss) VerifyWebhook(_ context.Context, body []byte) (*WebhookEvent, error) {
	if len(body) == 0 || len(body) > tossMaxBody {
		return nil, ErrWebhookUnverified
	}
	var raw struct {
		EventType string `json:"eventType"`
		Data      struct {
			PaymentKey  string `json:"paymentKey"`
			OrderID     string `json:"orderId"`
			Secret      string `json:"secret"`
			TotalAmount int    `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, ErrWebhookUnverified
	}
	if raw.EventType == "" || raw.Data.OrderID == "" {
		return nil, ErrWebhookUnverified
	}
	// 이벤트 ID 는 (eventType, orderId, paymentKey) 로 만든다. 토스는 별도
	// 이벤트 ID 를 주지 않으므로, 재전송 멱등(FR-610)의 키를 우리가 정해야
	// 한다 — webhook_events (pg, event_id) 유니크가 이 값을 받는다.
	return &WebhookEvent{
		EventID:    raw.EventType + ":" + raw.Data.OrderID + ":" + raw.Data.PaymentKey,
		Type:       raw.EventType,
		OrderNo:    raw.Data.OrderID,
		PaymentKey: raw.Data.PaymentKey,
		Secret:     raw.Data.Secret,
		Amount:     raw.Data.TotalAmount,
		Raw:        body,
	}, nil
}

func (t *Toss) post(ctx context.Context, path string, body map[string]any, idem string) (*Payment, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", t.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")
	// 모든 POST 에 붙는다 (D50). 없는 채로 재시도하면 두 번 승인된다.
	if idem != "" {
		httpReq.Header.Set("Idempotency-Key", idem)
	}
	return t.do(httpReq)
}

func (t *Toss) do(httpReq *http.Request) (*Payment, error) {
	resp, err := t.client.Do(httpReq)
	if err != nil {
		// 결과 불명이다. 재승인하지 않는다 — D50 은 조회 API 로 실제 상태를
		// 확인한 뒤 판단하라고 적었다.
		//
		// 원인 error 를 감싸지 않는다. URL 이 담기고, 그 URL 에는 경로에 실은
		// paymentKey 가 들어 있다.
		return nil, ErrPaymentUnknown
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, tossMaxBody))
	if err != nil {
		return nil, ErrPaymentUnknown
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.apiError(resp.StatusCode, raw)
	}

	var p struct {
		PaymentKey  string `json:"paymentKey"`
		OrderID     string `json:"orderId"`
		Status      string `json:"status"`
		TotalAmount int    `json:"totalAmount"`
		Secret      string `json:"secret"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, ErrPaymentUnknown
	}
	return &Payment{
		PaymentKey: p.PaymentKey,
		OrderNo:    p.OrderID,
		Status:     tossStatus(p.Status),
		Amount:     p.TotalAmount,
		Raw:        raw,
		Secret:     p.Secret,
	}, nil
}

// apiError turns a non-200 into one of our errors.
//
// 응답 본문을 오류 메시지에 싣지 않는다. PG 가 무엇을 돌려보낼지 우리가 정하지
// 못하고, 그 문자열이 로그와 화면으로 흘러간다. 코드만 옮긴다.
func (t *Toss) apiError(status int, raw []byte) error {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	switch e.Code {
	case "EXPIRED_PAYMENT", "REJECT_CARD_PAYMENT":
		return fmt.Errorf("%w (%s)", ErrPaymentExpired, e.Code)
	case "IDEMPOTENT_REQUEST_PROCESSING":
		// 이전 요청이 처리 중이다. 결과가 불명이므로 조회로 간다.
		return fmt.Errorf("%w (%s)", ErrPaymentUnknown, e.Code)
	}
	if status >= 500 {
		return fmt.Errorf("%w (HTTP %d)", ErrPaymentUnknown, status)
	}
	return fmt.Errorf("commerce: 결제 요청이 거부되었습니다 (HTTP %d, %s)", status, e.Code)
}

// tossStatus folds the gateway's status strings into our three.
//
// 모르는 값은 '대기' 다. '실패' 로 접으면 실제로는 승인된 결제를 실패로 기록해
// 물건이 나가지 않고, '승인' 으로 접으면 안 된 결제를 승인으로 기록해 물건이
// 나간다. '대기' 는 A-508 대사 대상이 되어 사람이 본다.
func tossStatus(s string) PaymentStatus {
	switch s {
	case "DONE":
		return PaymentApproved
	case "CANCELED", "PARTIAL_CANCELED", "ABORTED", "EXPIRED":
		return PaymentFailed
	default:
		return PaymentPending
	}
}

// compile-time check: Toss satisfies the contract.
var _ Gateway = (*Toss)(nil)
