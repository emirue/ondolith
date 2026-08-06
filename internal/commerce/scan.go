package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrScanFormat 은 스캔 값이 uuid 형식이 아니라는 뜻이다 (422).
	// 없는 조합(404)과 구분한다 — 형식 오류는 스캐너 설정 문제이고, 없는
	// 조합은 라벨이 오래된 것이라 고치는 사람이 다르다.
	ErrScanFormat = errors.New("commerce: 스캔 값이 조합 식별자 형식이 아닙니다")
	// ErrStockLedger 는 실사 중 장부가 바뀐 경우다 (409).
	ErrStockLedger = errors.New("commerce: 재고가 방금 바뀌었습니다")
	// ErrPickNotInOrder 는 주문에 없는 조합을 스캔한 경우다.
	ErrPickNotInOrder = errors.New("commerce: 이 주문에 없는 조합입니다")
	// ErrPickOverCount 는 주문 수량을 넘겨 스캔한 경우다.
	ErrPickOverCount = errors.New("commerce: 주문 수량을 넘었습니다")
)

// ScannedVariant is what A-514/A-515/A-517 show after a scan.
type ScannedVariant struct {
	ID           string
	ProductID    string
	ProductName  string
	OptionValues map[string]string
	SKU          string
	Stock        int
}

// ScanVariant resolves a scanned value.
//
// **QR 이 담는 것은 `product_variants.id` 다** (FR-620). SKU 로 찾지 않는다 —
// SKU 는 외부 시스템이 정하고 바뀔 수 있어서, 바뀌는 순간 이미 붙은 스티커가
// 다른 조합을 가리키거나 아무것도 가리키지 않게 된다.
func (s *Store) ScanVariant(ctx context.Context, scanned string) (*ScannedVariant, error) {
	if !looksLikeUUID(scanned) {
		return nil, fmt.Errorf("%w: %q", ErrScanFormat, scanned)
	}
	var v ScannedVariant
	var sku *string
	err := s.pool.QueryRow(ctx, `
		SELECT v.id, v.product_id, p.name, v.option_values, v.sku, v.stock
		FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE v.id = $1`, scanned).
		Scan(&v.ID, &v.ProductID, &v.ProductName, &v.OptionValues, &sku, &v.Stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if sku != nil {
		v.SKU = *sku
	}
	return &v, nil
}

// ReceiveStock is A-514's 입고.
//
// **단일 문장이다.** 읽고-더하고-쓰는 경로를 두면 동시 입고 두 건 중 하나가
// 다른 하나를 덮어쓴다 (FR-621) — 그리고 그 사고는 테스트가 통과하는 채로
// 일어난다.
func (s *Store) ReceiveStock(ctx context.Context, variantID string, qty int) (int, error) {
	if !looksLikeUUID(variantID) {
		return 0, fmt.Errorf("%w: %q", ErrScanFormat, variantID)
	}
	if qty < 1 || qty > 10000 {
		return 0, fmt.Errorf("%w: 수량 %d", ErrQuantityRange, qty)
	}
	var after int
	err := s.pool.QueryRow(ctx, `
		UPDATE product_variants SET stock = stock + $2, updated_at = now()
		WHERE id = $1 RETURNING stock`, variantID, qty).Scan(&after)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return after, nil
}

// StocktakeResult is what A-515 logs: 장부·실측·조정 세 값.
type StocktakeResult struct {
	Ledger  int
	Counted int
	Delta   int
}

// Stocktake is A-515's 재고 실사.
//
// **조정값은 서버가 `실측 - 장부` 로 계산한다** (D19 A-515 받지 않는 필드).
// 클라이언트가 조정값을 주면 실사가 임의 재고 조작 창구가 된다.
//
// 쓰기는 `SET stock = stock + $조정 WHERE id = $1 AND stock = $장부` 다 —
// **delta 로 쓰고 WHERE 로 잠근다.** 절대값 대입은 세는 동안 팔린 것을 지우고,
// WHERE 가 없으면 그 사이 들어온 주문이 조용히 사라진다.
func (s *Store) Stocktake(ctx context.Context, variantID string, counted, ledger int) (*StocktakeResult, error) {
	if !looksLikeUUID(variantID) {
		return nil, fmt.Errorf("%w: %q", ErrScanFormat, variantID)
	}
	// 0 이 유효하다 — "세어보니 없었다" 가 실사의 정상 결과다.
	if counted < 0 || counted > 1000000 {
		return nil, fmt.Errorf("%w: 실측 %d", ErrQuantityRange, counted)
	}

	delta := counted - ledger
	if delta == 0 {
		// 차이가 없다는 것도 실사의 결과다. 장부가 그 사이 바뀌지 않았는지는
		// 확인한다 — 안 하면 "차이 없음" 이 거짓이 될 수 있다.
		var now int
		err := s.pool.QueryRow(ctx,
			`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if now != ledger {
			return nil, fmt.Errorf("%w: 장부 %d, 현재 %d", ErrStockLedger, ledger, now)
		}
		return &StocktakeResult{Ledger: ledger, Counted: counted}, nil
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE product_variants SET stock = stock + $2, updated_at = now()
		WHERE id = $1 AND stock = $3`, variantID, delta, ledger)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return nil, ErrOutOfStock
	}
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		// 조합이 없거나 장부가 바뀌었다. 둘을 구분한다 — 운영자가 할 일이
		// 다르다 (라벨 교체 vs 다시 세기).
		var now int
		err := s.pool.QueryRow(ctx,
			`SELECT stock FROM product_variants WHERE id = $1`, variantID).Scan(&now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: 장부 %d, 현재 %d", ErrStockLedger, ledger, now)
	}
	return &StocktakeResult{Ledger: ledger, Counted: counted, Delta: delta}, nil
}

// PickLine is one line of A-516's 대조.
type PickLine struct {
	VariantID   string
	ProductName string
	OptionLabel string
	Ordered     int
}

// PickList reads what an order should ship.
//
// **이 화면은 재고도 주문 상태도 건드리지 않는다** (FR-623). 건드리기 시작하면
// 재고는 P-406 에서 이미 차감됐으므로 이중 차감이 되고, 상태는 A-506 이
// 옮기는 것이라 유령 전이가 생긴다 — 그래서 여기에는 UPDATE 가 없다.
func (s *Store) PickList(ctx context.Context, orderNo string) ([]PickLine, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT oi.variant_id, oi.product_name, oi.option_label, oi.quantity
		FROM order_items oi JOIN orders o ON o.id = oi.order_id
		WHERE o.order_no = $1 ORDER BY oi.product_name`, orderNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PickLine
	for rows.Next() {
		var l PickLine
		if err := rows.Scan(&l.VariantID, &l.ProductName, &l.OptionLabel, &l.Ordered); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// CheckPick validates one scan against the pick list and the tally so far.
//
// 순수 함수다 — 대조는 상태를 바꾸지 않으므로 DB 가 필요 없고, 그래서 이
// 규칙의 테스트도 DB 를 요구하지 않는다.
func CheckPick(lines []PickLine, scanned map[string]int, variantID string) error {
	for _, l := range lines {
		if l.VariantID != variantID {
			continue
		}
		if scanned[variantID] >= l.Ordered {
			return fmt.Errorf("%w: %s 는 %d개 주문인데 %d번째다",
				ErrPickOverCount, l.ProductName, l.Ordered, scanned[variantID]+1)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrPickNotInOrder, variantID)
}

// PickComplete reports whether every line has been scanned to its full count.
func PickComplete(lines []PickLine, scanned map[string]int) bool {
	for _, l := range lines {
		if scanned[l.VariantID] != l.Ordered {
			return false
		}
	}
	return len(lines) > 0
}

// looksLikeUUID checks the shape only.
//
// 파싱 라이브러리를 쓰지 않는 이유는 **여기서 하려는 것이 형식 구분뿐**이기
// 때문이다: 형식이 아니면 422, 형식인데 없으면 404. 실제 존재 확인은 DB 가
// 한다 (uuid 컬럼이 잘못된 값을 받으면 22P02 로 500 이 되므로 그 앞에서 막는다).
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
