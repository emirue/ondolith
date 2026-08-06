package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// WebhookRow is one row of A-603.
type WebhookRow struct {
	ID        string
	PG        string
	EventID   string
	OrderNo   string
	Status    string
	Payload   string
	Error     string
	CreatedAt time.Time
}

// WebhookHistory is A-603's list. **`수신` 이 먼저 온다** — 처리되지 않은 채
// 남은 행이 이 화면의 목적이고, D50 이 자동 재처리를 두지 않기로 했으므로
// 사람이 그것을 봐야 한다.
func (s *Store) WebhookHistory(ctx context.Context, limit int) ([]WebhookRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.pg, w.event_id, COALESCE(o.order_no, ''), w.status,
		       w.payload::text, COALESCE(w.error, ''), w.created_at
		FROM webhook_events w LEFT JOIN orders o ON o.id = w.order_id
		ORDER BY (w.status = '수신') DESC, w.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookRow
	for rows.Next() {
		var r WebhookRow
		if err := rows.Scan(&r.ID, &r.PG, &r.EventID, &r.OrderNo, &r.Status,
			&r.Payload, &r.Error, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReconcileRow is one line of A-508: 우리 기록과 PG 조회의 대조 결과.
type ReconcileRow struct {
	PaymentID string
	// PaymentKey 는 **PG 가 발급한 키**다. 조회 API 는 우리 PK 를 모른다 —
	// 우리 id 를 넘기면 언제나 "기록 없음" 이 나오고, 그것이 곧 "대사가
	// 늘 불일치를 보고하는" 화면이 된다.
	PaymentKey string
	OrderNo    string
	PG         string
	Kind       string
	// Ours 는 우리가 승인으로 기록한 금액이다. Theirs 는 조회 API 가 말한 것.
	OurStatus   string
	OurAmount   int
	TheirStatus string
	TheirAmount int
	// Diff 는 사람이 읽을 차이 설명이다. 비면 일치다.
	Diff      string
	CreatedAt time.Time
}

// PaymentsToReconcile lists the payments in a window.
//
// **`대기` 로 남은 행이 가장 위험하다** (D50): 승인 API 는 성공했는데 우리
// 트랜잭션이 실패한 경우가 그 모습이고, 그 돈은 나갔는데 주문은 결제대기다.
// 그래서 기간 필터와 무관하게 먼저 온다.
func (s *Store) PaymentsToReconcile(ctx context.Context, since, until time.Time) ([]ReconcileRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.payment_key, o.order_no, p.pg, p.kind, p.status,
		       p.approved_amount, p.created_at
		FROM payments p JOIN orders o ON o.id = p.order_id
		WHERE p.created_at >= $1 AND p.created_at < $2
		ORDER BY (p.status = '대기') DESC, p.created_at DESC
		LIMIT 500`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReconcileRow
	for rows.Next() {
		var r ReconcileRow
		if err := rows.Scan(&r.PaymentID, &r.PaymentKey, &r.OrderNo, &r.PG, &r.Kind,
			&r.OurStatus, &r.OurAmount, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reconcile asks the gateway what it thinks and reports the differences.
//
// **금액은 폼에서 받지 않는다** (D19 A-508 받지 않는 필드). 근거는 조회
// 결과이고, 폼에서 받으면 대사가 아니라 수기 조작이다.
//
// **자동으로 고치지 않는다.** 조회 결과를 우리 행에 그대로 쓰면 PG 의 일시적
// 응답 하나가 우리 장부를 바꾼다 — 사람이 보고 A-506 으로 옮긴다 (D50).
func (s *Store) Reconcile(ctx context.Context, gw Gateway, rows []ReconcileRow) []ReconcileRow {
	out := make([]ReconcileRow, 0, len(rows))
	for _, r := range rows {
		got, err := gw.Get(ctx, r.PaymentKey)
		switch {
		case err != nil:
			r.Diff = "조회 실패: " + err.Error()
		case got == nil:
			r.Diff = "PG 에 기록이 없다"
		default:
			r.TheirStatus, r.TheirAmount = string(got.Status), got.Amount
			switch {
			case r.OurStatus == "대기" && got.Status == PaymentApproved:
				// **가장 위험한 상태다** (D50). 돈은 나갔는데 주문은 결제대기다.
				r.Diff = "PG 는 승인, 우리는 대기 — 돈이 나갔는데 주문에 반영되지 않았다"
			case string(got.Status) != r.OurStatus:
				r.Diff = "상태 불일치"
			case got.Amount != r.OurAmount:
				r.Diff = "금액 불일치"
			}
		}
		out = append(out, r)
	}
	return out
}
