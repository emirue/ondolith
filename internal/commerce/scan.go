package commerce

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emirue/ondolith/internal/commerce/commerceq"
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
	// ErrPickAmbiguous 는 스캔 값이 둘 이상의 조합에 맞는 경우다 (FR-627).
	// 피킹에서 하나를 추측해 고르면 오출고가 대조 완료로 기록된다.
	ErrPickAmbiguous = errors.New("commerce: 여러 조합에 해당하는 코드입니다")
)

// ScannedVariant is what A-514/A-515/A-517 show after a scan.
type ScannedVariant struct {
	ID           string
	ProductID    string
	ProductName  string
	OptionValues map[string]string
	SKU          string
	Barcode      string
	Stock        int
}

// VariantQuery is what A-516·A-517 ask FindVariants.
type VariantQuery struct {
	// Q 는 스캔 값이거나 손으로 친 검색어다. 서버는 둘을 구분하지 않는다 —
	// 장치별 분기를 만들면 한쪽만 테스트된 경로가 생긴다 (D13).
	Q string
	// ProductID 는 한 상품으로 좁힌다 (A-501·A-502 의 「재고」 링크).
	ProductID string
	Page      int
}

// VariantMatches is FindVariants' answer.
type VariantMatches struct {
	Rows []ScannedVariant
	// Exact 는 Rows 가 QR·SKU·바코드 **정확 일치**로 찾은 것인지다. false 면
	// 상품명 부분 일치이거나 검색어가 없는 전체 목록이다.
	Exact bool
	More  bool
}

// FindVariants is **the one identification function** (FR-627, D13 「식별 값」).
//
// QR(`product_variants.id`)·SKU·바코드 정확 일치를 먼저 보고, 하나도 없을 때만
// 상품명 부분 일치를 돌려준다. 규칙은 queries/scan.sql FindVariants 한 문장에
// 있고 A-516·A-517 이 이 함수만 쓴다 — 화면마다 따로 찾으면 규칙이 갈라진다.
//
// **맞는 것이 없는 것은 오류가 아니다.** 형식이 uuid 가 아닌 ProductID 도 빈
// 결과다: 검색은 사람이 치는 값이고, 못 찾은 것은 검색 결과다.
func (s *Store) FindVariants(ctx context.Context, q VariantQuery) (*VariantMatches, error) {
	arg := commerceq.FindVariantsParams{Q: strings.TrimSpace(q.Q)}
	if looksLikeUUID(arg.Q) {
		arg.ID = &arg.Q
	}
	if q.ProductID != "" {
		if !looksLikeUUID(q.ProductID) {
			return &VariantMatches{}, nil
		}
		arg.ProductID = &q.ProductID
	}
	limit, offset := ProductQuery{Page: q.Page}.clamp()
	arg.Limit, arg.Offset = int32(limit+1), int32(offset)

	rows, err := s.q.FindVariants(ctx, arg)
	if err != nil {
		return nil, err
	}
	out := &VariantMatches{More: len(rows) > limit}
	if out.More {
		rows = rows[:limit]
	}
	for _, r := range rows {
		v := ScannedVariant{ID: r.ID, ProductID: r.ProductID, ProductName: r.ProductName,
			SKU: r.Sku, Barcode: r.Barcode, Stock: int(r.Stock)}
		if err := unmarshalOptions(r.OptionValues, &v.OptionValues); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, v)
		out.Exact = r.Exact
	}
	return out, nil
}

// One picks the single combination a scanned value names (A-516).
//
// **상품명 부분 일치는 쓰지 않는다** — 피킹은 손에 든 물건이 맞는지 확인하는
// 일인데, 이름으로 고르면 확인이 아니라 선택이 된다. 둘 이상에 맞으면 고르지
// 않고 거부한다.
func (m *VariantMatches) One() (*ScannedVariant, error) {
	switch {
	case !m.Exact || len(m.Rows) == 0:
		return nil, ErrPickNotInOrder
	case len(m.Rows) > 1:
		return nil, ErrPickAmbiguous
	}
	return &m.Rows[0], nil
}

// ScanVariant reads one combination by its id — A-514·A-515 가 행이 실어 온
// `variant_id` 로 상품·조합 이름을 읽는 데 쓴다.
//
// **값을 해석하지 않는다.** SKU·바코드·이름으로 조합을 찾는 것은 FindVariants
// 하나의 일이다. uuid 형식이 아니면 ErrScanFormat(422), 없으면 ErrNotFound(404).
func (s *Store) ScanVariant(ctx context.Context, scanned string) (*ScannedVariant, error) {
	if !looksLikeUUID(scanned) {
		return nil, fmt.Errorf("%w: %q", ErrScanFormat, scanned)
	}
	r, err := s.q.ScanVariant(ctx, scanned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := ScannedVariant{ID: r.ID, ProductID: r.ProductID, ProductName: r.ProductName, Stock: int(r.Stock)}
	if err := unmarshalOptions(r.OptionValues, &v.OptionValues); err != nil {
		return nil, err
	}
	if r.Sku != nil {
		v.SKU = *r.Sku
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
	after, err := s.q.ReceiveStock(ctx, commerceq.ReceiveStockParams{ID: variantID, Qty: int32(qty)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return int(after), nil
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
	// 장부 값도 폼에서 온다. 재고는 integer 라 그 밖의 값은 맞을 수 없고,
	// int32 변환이 그것을 다른 수로 접는다.
	if ledger < 0 || ledger > MaxAmount {
		return nil, fmt.Errorf("%w: 장부 %d", ErrQuantityRange, ledger)
	}

	delta := counted - ledger
	if delta == 0 {
		// 차이가 없다는 것도 실사의 결과다. 장부가 그 사이 바뀌지 않았는지는
		// 확인한다 — 안 하면 "차이 없음" 이 거짓이 될 수 있다.
		now, err := s.q.VariantStock(ctx, variantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if int(now) != ledger {
			return nil, fmt.Errorf("%w: 장부 %d, 현재 %d", ErrStockLedger, ledger, now)
		}
		return &StocktakeResult{Ledger: ledger, Counted: counted}, nil
	}

	n, err := s.q.StocktakeAdjust(ctx, commerceq.StocktakeAdjustParams{
		ID: variantID, Delta: int32(delta), Ledger: int32(ledger)})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return nil, ErrOutOfStock
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// 조합이 없거나 장부가 바뀌었다. 둘을 구분한다 — 운영자가 할 일이
		// 다르다 (라벨 교체 vs 다시 세기).
		now, err := s.q.VariantStock(ctx, variantID)
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
	rows, err := s.q.PickList(ctx, orderNo)
	if err != nil {
		return nil, err
	}
	var out []PickLine
	for _, r := range rows {
		out = append(out, PickLine{VariantID: r.VariantID, ProductName: r.ProductName,
			OptionLabel: r.OptionLabel, Ordered: int(r.Quantity)})
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// CheckPick validates one scan against the pick list and the tally so far.
//
// qty 는 한 번에 세는 개수다 (기본 1). **누적이 주문 수량을 넘으면 거부한다** —
// 3개 주문에 2개를 센 뒤 2개를 더 세는 것은 4번째 물건이 상자에 들어간다는 뜻이다.
//
// 순수 함수다 — 대조는 상태를 바꾸지 않으므로 DB 가 필요 없고, 그래서 이
// 규칙의 테스트도 DB 를 요구하지 않는다.
func CheckPick(lines []PickLine, scanned map[string]int, variantID string, qty int) error {
	if qty < 1 {
		return fmt.Errorf("%w: 수량 %d", ErrQuantityRange, qty)
	}
	for _, l := range lines {
		if l.VariantID != variantID {
			continue
		}
		if scanned[variantID]+qty > l.Ordered {
			return fmt.Errorf("%w: %s 는 %d개 주문인데 %d개째다",
				ErrPickOverCount, l.ProductName, l.Ordered, scanned[variantID]+qty)
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
