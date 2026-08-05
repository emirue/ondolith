package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrAlreadyPaid is FR-608 이 DB 유니크로 막은 것을 옮긴 이름이다.
	ErrAlreadyPaid = errors.New("commerce: 이미 결제된 주문입니다")
	// ErrAuthWindowClosed is D50 의 10분 창을 넘긴 경우.
	ErrAuthWindowClosed = errors.New("commerce: 결제 인증 시간이 지났습니다")
	// ErrPaymentKeyReused 는 같은 승인 키가 이미 기록돼 있다는 뜻이다.
	// "이미 결제된 주문" 과 다르다 — 이쪽은 PG 쪽 이상이거나 우리가 키를
	// 잘못 옮긴 것이고, A-508 대사가 볼 대상이다.
	ErrPaymentKeyReused = errors.New("commerce: 이미 기록된 승인 키입니다")
)

// AuthWindow is D50's 10분. 리다이렉트 후 이 시간 안에 승인 API 를 불러야 한다.
const AuthWindow = 10 * time.Minute

// ConfirmPayment is P-408's body.
//
// 순서가 방어다. D50 결제 흐름의 ★ 표시 두 개가 **승인 API 호출보다 앞**에
// 있어야 한다 — 호출한 뒤에 대조하면 돈은 이미 나갔고, 되돌리는 것은 취소
// API 이지 검증이 아니다.
//
//  1. 주문을 잠근 채 읽는다.
//  2. 금액 대조 (FR-607). 다르면 **호출하지 않는다.**
//  3. 10분 창 확인. 넘었으면 **호출하지 않는다.**
//  4. payments 행을 '대기' 로 선점한다 — 여기서 DB 유니크가 동시 두 건 중
//     하나를 떨어뜨린다 (FR-608). 애플리케이션 검사가 아니라 이것이 막는다.
//  5. 승인 API 호출.
//  6. 결과 기록 + 상태 전이 (상태머신을 거친다).
func (s *Store) ConfirmPayment(ctx context.Context, gw Gateway, pgName string,
	orderNo, paymentKey string, callbackAmount int, now time.Time) (*Payment, error) {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// (1) 주문을 읽는다.
	//
	// 여기에 `FOR UPDATE` 를 두었었는데 지웠다. 이 트랜잭션은 승인 API 를
	// 부르기 **전에** 커밋하므로 잠금은 그 시점에 풀리고, 아래 (6)의 전이는
	// 잠금 밖에서 일어난다 — 즉 잠금이 지키는 것이 없었다. 동시 승인 두 건은
	// 결제 유니크가 막고, 읽은 뒤 상태가 바뀌는 것은 (6)의 비교-교환이 막는다.
	var orderID, status string
	var stored int
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, status, total_amount, created_at FROM orders
		WHERE order_no = $1`, orderNo).Scan(&orderID, &status, &stored, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// (2) FR-607. 승인 API 를 부르기 전이다.
	if err := VerifyAmount(stored, callbackAmount); err != nil {
		return nil, err
	}

	// (3) D50 의 10분. 넘겼으면 부르지 않고 주문을 결제대기에 둔 채 돌려보낸다
	// — 재시도 경로를 남기기 위해서다 (P-409).
	if now.Sub(createdAt) > AuthWindow {
		return nil, ErrAuthWindowClosed
	}

	// (4) 선점. 부분 유니크(주문당 살아 있는 주문결제 1건)가 여기서 두 번째를
	// 떨어뜨린다. 애플리케이션의 "이미 승인됐나?" 검사는 두 요청이 같은 답을
	// 읽고 둘 다 진행하므로 막지 못한다 (D50 「멱등성」).
	var paymentID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (order_id, kind, status, pg, payment_key, approved_amount)
		VALUES ($1, '주문결제', '대기', $2, $3, $4) RETURNING id`,
		orderID, pgName, paymentKey, stored).Scan(&paymentID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// 어느 유니크에 걸렸는지 구분한다. 둘을 한 오류로 접으면 "이 주문은
		// 이미 결제됐습니다" 가 실제로는 **다른 주문**의 승인 키가 겹쳤다는
		// 뜻일 수 있고, 그러면 사람이 엉뚱한 주문을 들여다본다.
		if pgErr.ConstraintName == "payments_pg_key_idx" {
			return nil, fmt.Errorf("%w: %s/%s", ErrPaymentKeyReused, pgName, paymentKey)
		}
		return nil, ErrAlreadyPaid
	}
	if err != nil {
		return nil, err
	}

	// 선점을 커밋한다. 승인 API 호출은 네트워크 왕복이라, 트랜잭션을 열어 둔 채
	// 부르면 주문 행 잠금이 그 시간만큼 유지되고 다른 요청이 전부 줄을 선다.
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// (5) 승인.
	res, err := gw.Confirm(ctx, ConfirmRequest{
		PaymentKey: paymentKey, OrderNo: orderNo, Amount: stored,
		// 멱등키는 결제 건 ID 다. 재시도해도 같은 값이라 PG 쪽이 첫 결과를
		// 돌려준다 — 새로 만들면 재시도가 두 번째 승인이 된다.
		IdempotencyKey: paymentID,
	})
	if err != nil {
		// 결과 불명(타임아웃·5xx)은 '대기' 로 남긴다. 그것이 A-508 대사 대상이고,
		// 재승인 대신 조회 API 로 확인한다 (D50).
		if errors.Is(err, ErrPaymentUnknown) {
			return nil, err
		}
		// 확정된 실패만 '실패' 로 내린다. 부분 유니크의 `status <> '실패'` 가
		// 재결제 경로를 열어 준다.
		if _, uerr := s.pool.Exec(ctx,
			`UPDATE payments SET status = '실패', updated_at = now() WHERE id = $1`,
			paymentID); uerr != nil {
			return nil, uerr
		}
		return nil, err
	}

	// (6) 결과 기록과 상태 전이. 한 트랜잭션이다 — 결제는 승인됐는데 주문이
	// 결제대기로 남는 상태를 만들지 않는다.
	tx2, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx2.Rollback(ctx)

	// 승인 응답 금액도 대조한다. PG 가 요청과 다른 금액을 확정하는 것은 정상
	// 동작이 아니지만, 정상이 아닌 것을 기록만 하고 넘어가면 그때부터 우리
	// 장부와 PG 장부가 다르다.
	if err := VerifyAmount(stored, res.Amount); err != nil {
		return nil, err
	}

	// 상태 전이는 상태머신을 거친다. 여기서 UPDATE 를 직접 쓰면 D14 5절의
	// 표가 이 경로에는 적용되지 않는다.
	if err := CanTransition(Status(status), StatusPaid, "P-408"); err != nil {
		return nil, err
	}

	// 비교-교환이다. 위에서 읽은 상태가 그대로일 때만 옮긴다.
	//
	// 상태는 승인 API 왕복 **전에** 읽은 값이다. 그 사이 A-507 이 취소로 옮겼을
	// 수 있고, 조건 없는 UPDATE 는 취소된 주문을 결제완료로 되돌린다 — 역전이
	// 금지(D14)를 코드가 스스로 어기는 경로다.
	tag, err := tx2.Exec(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1 AND status = $3`,
		orderID, string(StatusPaid), status)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("%w: 승인하는 사이 주문 상태가 %s 에서 바뀌었습니다",
			ErrTransitionNotAllowed, status)
	}
	if _, err := tx2.Exec(ctx, `
		UPDATE payments SET status = $2, approved_at = now(), raw_response = $3, updated_at = now()
		WHERE id = $1`, paymentID, string(res.Status), MaskCardFields(res.Raw)); err != nil {
		return nil, err
	}

	if err := tx2.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// cardish matches anything long enough to be a card number, with or without
// separators. 뒷자리만 남긴 값도 잡는다 — D81 W3-16 이 "카드 뒷자리 이상이
// 들어오면 마스킹" 이라고 적었다.
var cardish = regexp.MustCompile(`\d[\d \-]{10,}\d`)

// maskedKeys are the JSON keys whose values are replaced outright.
//
// 값의 모양만 보고 지우는 것으로는 부족하다. `"cardNumber": "1234"` 는 짧아서
// 위 정규식에 걸리지 않지만 카드 정보이고, 키 이름이 그것을 말해 준다.
var maskedKeys = map[string]bool{
	"cardnumber": true, "number": true, "pan": true,
	"cvc": true, "cvv": true, "expirydate": true, "expyear": true, "expmonth": true,
}

// MaskCardFields removes card data from a gateway response before it is stored.
//
// DEC-3.7 / PCI DSS: 카드번호·유효기간·CVC 는 컬럼도 없고 raw_response 에도
// 넣지 않는다. 어댑터가 무엇을 돌려보낼지 우리가 정하지 못하므로, 저장 직전에
// 한 번 더 거른다.
//
// 파싱이 실패하면 **통째로 버린다.** 모르는 모양을 그대로 저장하는 것이
// 마스킹 실패의 가장 흔한 형태다.
func MaskCardFields(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(`{"masked":"파싱 실패로 원문을 버렸습니다"}`)
	}
	out, err := json.Marshal(maskValue(v))
	if err != nil {
		return []byte(`{"masked":"직렬화 실패로 원문을 버렸습니다"}`)
	}
	return out
}

func maskValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if maskedKeys[lower(k)] {
				t[k] = "[가림]"
				continue
			}
			t[k] = maskValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = maskValue(child)
		}
		return t
	case string:
		return cardish.ReplaceAllString(t, "[가림]")
	default:
		return v
	}
}

// lower is ASCII-only on purpose: JSON keys from a payment gateway are ASCII,
// and strings.ToLower would pull in Unicode case folding for no gain.
func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// FailPayment records a confirmed failure without transitioning the order.
//
// D14 5-1: P-409 는 결제 시도만 기록한다. 주문은 결제대기에 머문다 — 재시도
// 경로를 남기기 위해서이고, 역전이 금지 규칙과도 맞는다.
func (s *Store) FailPayment(ctx context.Context, orderNo, reason string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE payments SET status = '실패', updated_at = now()
		WHERE order_id = (SELECT id FROM orders WHERE order_no = $1)
		  AND kind = '주문결제' AND status = '대기'`, orderNo)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, orderNo)
	}
	return nil
}
