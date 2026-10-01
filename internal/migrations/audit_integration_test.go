package migrations

import (
	"context"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// 00023 이 더한 제약·트리거가 실제로 거부하는지 본다. 각 경우는 그 전까지
// 데이터베이스가 받아 주던 문장이다 (2026-10 감사 실측).

// 「주문·품목·결제·환불 행은 지워지지 않는다」(D30). RESTRICT 가 지킨 것은 자식이
// 있는 부모뿐이라, 자식부터 지우면 전부 지워졌다.
func TestMoneyRowsCannotBeDeletedEvenWithoutChildren(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	orderID, itemID := seedPayable(t, pool)
	var payID, refundID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO payments (order_id,kind,status,pg,payment_key,approved_amount)
		VALUES ($1,'주문결제','승인','toss','pk-1',26000) RETURNING id`, orderID).Scan(&payID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO refunds (order_id,payment_id,status,requester,amount,request_key)
		VALUES ($1,$2,'요청','관리자',1000,'rk-1') RETURNING id`, orderID, payID).Scan(&refundID); err != nil {
		t.Fatal(err)
	}

	// 자식부터 — RESTRICT 가 걸리지 않는 순서다.
	for _, c := range []struct{ table, id string }{
		{"refunds", refundID}, {"payments", payID}, {"order_items", itemID}, {"orders", orderID},
	} {
		_, err := pool.Exec(ctx, `DELETE FROM `+c.table+` WHERE id = $1`, c.id)
		if err == nil {
			t.Errorf("%s 행이 지워졌다", c.table)
			continue
		}
		if !strings.Contains(err.Error(), "지울 수 없습니다") {
			t.Errorf("%s: 막은 것이 삭제 금지 트리거가 아니다: %v", c.table, err)
		}
	}
}

// D15 7절: 행 트리거는 TRUNCATE 를 보지 못한다.
func TestOperationLogCannotBeTruncated(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operation_logs (actor_email, action, target_type, summary)
		VALUES ('a@example.com', 'settings.update', 'settings', '기록')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE operation_logs`); err == nil {
		t.Error("작업 로그가 TRUNCATE 로 비워졌다")
	}
	if n := count(t, pool, `SELECT count(*) FROM operation_logs`); n != 1 {
		t.Errorf("TRUNCATE 뒤 %d행, want 1", n)
	}
}

// 회원 프로필 값·항목 정의의 모양. posts.custom_fields·board_fields.options 에는
// 처음부터 있던 제약이다.
func TestUserFieldShapesAreEnforced(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (email, password_hash) VALUES ('a@example.com','h')`); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"객체가 아닌 프로필 값":    `UPDATE users SET custom_fields = '[]'`,
		"16KB 를 넘는 프로필 값": `UPDATE users SET custom_fields = jsonb_build_object('k', repeat('x', 17000))`,
		"배열이 아닌 선택지":      `INSERT INTO user_fields (key,label,field_type,options) VALUES ('a','A','select','"x"')`,
		"4KB 를 넘는 선택지":    `INSERT INTO user_fields (key,label,field_type,options) VALUES ('b','B','select', jsonb_build_array(repeat('x', 5000)))`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%s 이 들어갔다", name)
		}
	}
	for name, q := range map[string]string{
		"객체 프로필 값": `UPDATE users SET custom_fields = '{"소속":"개발"}'`,
		"배열 선택지":   `INSERT INTO user_fields (key,label,field_type,options) VALUES ('c','C','select','["가","나"]')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Errorf("%s 이 막혔다: %v", name, err)
		}
	}
}

// 인덱스 셋: 외래키 둘에 새로 생겼고, 유니크의 앞머리와 겹치던 하나가 사라졌다.
func TestAuditIndexes(t *testing.T) {
	db, pool := testDB(t)
	if err := Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int64{
		"returns_new_variant_idx":            1,
		"role_permissions_permission_id_idx": 1,
		"return_items_return_idx":            0,
		"terms_kind_effective_uniq":          1,
		"terms_kind_idx":                     0,
		// 성능 (게시판·회원)
		"posts_board_title_desc_idx": 1,
		"posts_board_published_idx":  1,
		"users_created_idx":          1,
	} {
		if n := count(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, name); n != want {
			t.Errorf("인덱스 %s: %d개, want %d", name, n, want)
		}
	}
}

// 00023 은 (종류, 시행 시각) 유니크를 건다. 이미 겹친 행이 있는 사이트에서 그
// 인덱스 생성이 실패하면 업그레이드가 부팅에서 멈춘다 — 나중에 만든 쪽을 밀어
// 시행본으로 삼는다.
func TestDuplicateTermEffectiveTimesSurviveTheUpgrade(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO terms (kind, version, body, effective_at, created_at) VALUES
		('이용약관','v1','옛 본문', '2030-01-01', '2029-01-01'),
		('이용약관','v2','새 본문', '2030-01-01', '2029-06-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("겹친 시행 시각이 있는 DB 에서 업그레이드 실패: %v", err)
	}
	var version string
	if err := pool.QueryRow(ctx, `
		SELECT version FROM terms WHERE kind = '이용약관' ORDER BY effective_at DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "v2" {
		t.Errorf("시행본 %q, want v2 — 나중에 등록한 쪽이 시행본이어야 한다", version)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO terms (kind, version, body, effective_at, created_at)
		VALUES ('이용약관','v3','본문', '2030-01-01', '2029-07-01')`); err == nil {
		t.Error("같은 종류·같은 시행 시각의 약관이 또 들어갔다")
	}
}

// FR-618: 실패한 차액 결제는 자리를 비운다. 살아 있는 것은 여전히 하나다.
func TestFailedExchangePaymentDoesNotBlockRetry(t *testing.T) {
	db, pool := testDB(t)
	ctx := context.Background()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	orderID, itemID := seedPayable(t, pool)
	var returnID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO returns (return_no, order_id, kind, status, reason, new_variant_id, price_difference)
		SELECT 'R-0001', $1, '교환', '교환접수', '사이즈', variant_id, 2000 FROM order_items WHERE id = $2
		RETURNING id`, orderID, itemID).Scan(&returnID); err != nil {
		t.Fatal(err)
	}
	ins := func(key, status string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO payments (order_id,return_id,kind,status,pg,payment_key,approved_amount)
			VALUES ($1,$2,'교환차액',$3,'toss',$4,2000)`, orderID, returnID, status, key)
		return err
	}
	if err := ins("try-1", "실패"); err != nil {
		t.Fatal(err)
	}
	if err := ins("try-2", "대기"); err != nil {
		t.Errorf("실패한 뒤의 재결제가 막혔다: %v", err)
	}
	if err := ins("try-3", "대기"); err == nil {
		t.Error("살아 있는 차액 결제가 둘 들어갔다")
	}
}
