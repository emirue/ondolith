package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrWebhookDuplicate 는 이미 받은 이벤트라는 뜻이다. **오류가 아니라 정상
// 응답의 근거다** — 재전송에 멱등이어야 하므로 200 으로 답한다 (FR-610).
var ErrWebhookDuplicate = errors.New("commerce: 이미 받은 웹훅 이벤트입니다")

// RecordWebhook writes the event and returns its row id.
//
// **판단은 애플리케이션 조회가 아니라 유니크 인덱스가 한다.** `SELECT` 로 먼저
// 봐서는 동시에 도착한 재전송 두 건이 함께 통과한다 — 둘 다 "없다" 를 읽기
// 때문이다. 여기서 기대하는 실패는 23505 이고, 그것이 곧 멱등이다.
//
// 주문 연결은 **우리가 발급한 order_no 로만** 찾는다. 페이로드의 내부 PK 는
// 형식상 있어도 읽지 않는다 (D19 P-905 받지 않는 필드). 못 찾으면 NULL 로
// 남긴다 — 우리 주문이 아닌 알림도 기록은 남아야 A-603 에서 보인다.
func (s *Store) RecordWebhook(ctx context.Context, pg string, ev *WebhookEvent) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO webhook_events (pg, event_id, order_id, status, payload)
		VALUES ($1, $2, (SELECT id FROM orders WHERE order_no = $3), '수신', $4)
		RETURNING id`, pg, ev.EventID, ev.OrderNo, ev.Raw).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrWebhookDuplicate
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// ProcessWebhook is the half that runs after the 200 went out.
//
// **웹훅 본문을 진실로 채택하지 않는다** (D19 P-905 받지 않는 필드): 금액은
// 저장된 주문 금액과 **대조만** 하고, 상태 문자열은 그대로 넣지 않는다.
// 외부가 `구매확정` 을 지정할 수 있으면 취소·환불 경로가 임의로 닫힌다.
//
// 어떤 결과든 `webhook_events.status` 에 남긴다. 남기지 않으면 A-603 이
// 「처리되지 않은 웹훅」을 보여줄 수 없고, D50 이 자동 재처리를 두지 않기로
// 한 이상 사람이 보는 그 목록이 유일한 복구 수단이다.
func (s *Store) ProcessWebhook(ctx context.Context, eventID string, ev *WebhookEvent) error {
	err := s.processWebhook(ctx, ev)
	status, msg := "처리완료", ""
	if err != nil {
		status, msg = "실패", err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	if _, uerr := s.pool.Exec(ctx, `
		UPDATE webhook_events SET status = $2, error = NULLIF($3, ''), updated_at = now()
		WHERE id = $1`, eventID, status, msg); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

func (s *Store) processWebhook(ctx context.Context, ev *WebhookEvent) error {
	var orderID, orderStatus string
	var total int
	err := s.pool.QueryRow(ctx,
		`SELECT id, status, total_amount FROM orders WHERE order_no = $1`, ev.OrderNo).
		Scan(&orderID, &orderStatus, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		// 우리 주문이 아니다. 기록은 남았고 그 이상 할 일이 없다.
		return fmt.Errorf("%w: 주문 %q", ErrNotFound, ev.OrderNo)
	}
	if err != nil {
		return err
	}

	// **secret 대조.** 승인 응답이 준 값과 같아야 한다 (D50). 이것만으로 진실을
	// 삼지는 않지만, 다르면 우리 결제에 대한 알림이 아니다.
	var stored string
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(secret, '') FROM payments
		WHERE order_id = $1 AND kind = '주문결제' AND status = '승인'`, orderID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: 승인된 결제가 없다", ErrNoPayment)
	}
	if err != nil {
		return err
	}
	if stored != "" && ev.Secret != stored {
		return errors.New("commerce: 웹훅 secret 이 승인 응답과 다릅니다")
	}

	// 금액은 **대조만** 한다. 웹훅이 알려준 값을 저장하지 않는다 (FR-610).
	if ev.Amount > 0 && ev.Amount != total {
		return fmt.Errorf("%w: 주문 %d, 웹훅 %d", ErrAmountMismatch, total, ev.Amount)
	}
	return nil
}
