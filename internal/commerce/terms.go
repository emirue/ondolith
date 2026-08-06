package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrTermsVersionTaken 은 (종류, 버전) 이 겹친 경우다. 겹치면 어느 본문에
	// 동의했는지 특정할 수 없다 (FR-619).
	ErrTermsVersionTaken = errors.New("commerce: 이미 있는 약관 버전입니다")
	// ErrTermsBackdated 는 시행일이 과거인 경우다. 소급이 되면 "주문 시점에
	// 유효했던 약관" 이 나중에 바뀔 수 있다 (D50).
	ErrTermsBackdated = errors.New("commerce: 시행일은 오늘 이후여야 합니다")
)

// Terms is one row of terms.
type Terms struct {
	ID          string
	Kind        string
	Version     string
	Body        string
	EffectiveAt time.Time
	Required    bool
	CreatedAt   time.Time
	// InUse 는 이 버전에 동의한 주문이 있다는 뜻이다. 화면이 "수정 불가" 를
	// 그리는 데 쓴다 — 거부하는 것은 여전히 서버다.
	InUse bool
}

// ListTerms is A-207's table, newest first per kind.
func (s *Store) ListTerms(ctx context.Context) ([]Terms, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.kind, t.version, t.body, t.effective_at, t.is_required, t.created_at,
		       EXISTS (SELECT 1 FROM order_agreements a WHERE a.terms_id = t.id)
		FROM terms t ORDER BY t.kind, t.effective_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Terms
	for rows.Next() {
		var t Terms
		if err := rows.Scan(&t.ID, &t.Kind, &t.Version, &t.Body, &t.EffectiveAt,
			&t.Required, &t.CreatedAt, &t.InUse); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTerms writes a new version.
//
// **기존 버전을 고치는 경로는 없다.** `order_agreements` 가 가리키는 본문이
// 바뀌면 동의 이력이 거짓이 된다 (D13, FR-619) — 그래서 이 파일에는 UPDATE 가
// 없고, 개정은 새 행이다.
func (s *Store) AddTerms(ctx context.Context, t Terms, now time.Time) (string, error) {
	if t.Kind == "" || t.Version == "" || t.Body == "" {
		return "", fmt.Errorf("commerce: 종류·버전·본문이 필요합니다")
	}
	// 소급 금지. DB CHECK (`effective_at >= created_at`) 도 막지만 거기서
	// 나오는 것은 제약 위반이라 화면이 500 을 그린다 — 운영자가 고칠 수 있는
	// 값이므로 설명이 있어야 한다.
	//
	// **오늘은 "지금부터" 로 읽는다.** 화면이 받는 것은 날짜이고 그 날짜는
	// 자정을 뜻하는데, 자정은 저장 시각보다 앞이라 DB CHECK 에 걸린다 —
	// 그대로 두면 운영자는 오늘을 영원히 고를 수 없다. 소급이 아니므로
	// (아직 시행된 적 없다) 시각만 지금으로 올린다.
	//
	// 올리는 것은 **DB 의 now() 로** 한다. Go 의 now 로 올리면 INSERT 가
	// 실행되는 시점에는 이미 과거라, 같은 CHECK 에 다시 걸린다.
	if t.EffectiveAt.Before(now) && !sameDay(t.EffectiveAt, now) {
		return "", fmt.Errorf("%w: %s", ErrTermsBackdated, t.EffectiveAt.Format("2006-01-02"))
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO terms (kind, version, body, effective_at, is_required)
		VALUES ($1, $2, $3, GREATEST($4::timestamptz, now()), $5) RETURNING id`,
		t.Kind, t.Version, t.Body, t.EffectiveAt, t.Required).Scan(&id)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return "", ErrTermsVersionTaken
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "terms_no_backdate":
		return "", ErrTermsBackdated
	case err != nil:
		return "", err
	}
	return id, nil
}

// RequiredTerms is what P-405 shows for agreement: 종류마다 시행된 최신 필수 약관.
//
// 시행일이 미래인 버전은 아직 유효하지 않다 — 등록해 두고 그날부터 적용된다.
func (s *Store) RequiredTerms(ctx context.Context, now time.Time) ([]Terms, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (kind) id, kind, version, body, effective_at, is_required, created_at
		FROM terms
		WHERE is_required AND effective_at <= $1
		ORDER BY kind, effective_at DESC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Terms
	for rows.Next() {
		var t Terms
		if err := rows.Scan(&t.ID, &t.Kind, &t.Version, &t.Body, &t.EffectiveAt,
			&t.Required, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TermsByID reads one version.
func (s *Store) TermsByID(ctx context.Context, id string) (*Terms, error) {
	var t Terms
	err := s.pool.QueryRow(ctx, `
		SELECT id, kind, version, body, effective_at, is_required, created_at
		FROM terms WHERE id = $1`, id).
		Scan(&t.ID, &t.Kind, &t.Version, &t.Body, &t.EffectiveAt, &t.Required, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// BusinessKeys are D50's fixed eight (전자상거래법 표시 의무 항목).
//
// 자유 키/값으로 두지 않는다 — 빠뜨린 항목을 아무도 모르고, `shop` 모드
// 미입력 경고(FR-711)를 띄울 대상도 정할 수 없다.
var BusinessKeys = []string{
	"business.name",       // 상호
	"business.owner",      // 대표자명
	"business.reg_no",     // 사업자등록번호
	"business.sales_no",   // 통신판매업신고번호
	"business.address",    // 주소
	"business.phone",      // 전화번호
	"business.email",      // 이메일
	"business.privacy_on", // 개인정보관리책임자
}

// BusinessLabels names them for the form and the footer.
var BusinessLabels = map[string]string{
	"business.name":       "상호",
	"business.owner":      "대표자명",
	"business.reg_no":     "사업자등록번호",
	"business.sales_no":   "통신판매업신고번호",
	"business.address":    "주소",
	"business.phone":      "전화번호",
	"business.email":      "이메일",
	"business.privacy_on": "개인정보관리책임자",
}

// MissingBusinessKeys reports which of the eight are empty.
//
// **저장을 거부하지 않는다** (D19 A-208). 설치 직후는 항상 비어 있고, 거부하면
// 운영자가 사업자 정보를 다 채우기 전에는 아무 설정도 저장할 수 없다. 대신
// `shop` 모드에서 경고 대상이 된다 (FR-711).
func MissingBusinessKeys(kv map[string]string) []string {
	var missing []string
	for _, k := range BusinessKeys {
		if kv[k] == "" {
			missing = append(missing, BusinessLabels[k])
		}
	}
	return missing
}

// sameDay compares calendar days in the value's own location.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.In(a.Location()).Date()
	return ay == by && am == bm && ad == bd
}
