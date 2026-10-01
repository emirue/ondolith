-- +goose Up

-- 2026-10 저장 계층 감사의 조치를 한 파일에 모은다. 절마다 무엇을 실측했는지 적는다.

-- ── 정합성 ──────────────────────────────────────────────────────────────────

-- FR-618: 교환 건당 **살아 있는** 차액 결제 1건. `status <> '실패'` 가 없어서 승인이
-- 한 번 실패하면 그 행이 자리를 영구히 차지했고, 재결제는 23505 → 「이미 결제된
-- 주문」으로만 끝났다. 주문결제(payments_order_approved_idx)와 같은 모양으로 맞춘다 —
-- 동시 두 건은 둘 다 '대기' 로 들어가려다 하나만 성공하므로 FR-618 은 그대로다.
DROP INDEX payments_exchange_idx;
CREATE UNIQUE INDEX payments_exchange_idx ON payments (order_id, return_id)
    WHERE kind = '교환차액' AND status <> '실패';

-- FR-619: 한 종류에 시행 시각이 같은 약관이 둘이면 「시행 중인 버전」이 정해지지
-- 않는다 — 주문서가 보여 준 버전과 주문이 요구한 버전이 다를 수 있었다. 유니크로
-- 막는다. 이미 겹친 행이 있으면 인덱스를 만들 수 없으므로, 나중에 만든 쪽을
-- 1마이크로초씩 뒤로 민다 (나중에 등록한 것이 시행본이 된다 — 질의의 동률 규칙과
-- 같다). 본문·버전은 건드리지 않는다.
UPDATE terms t SET effective_at = t.effective_at + (d.n - 1) * interval '1 microsecond'
FROM (SELECT id, row_number() OVER (PARTITION BY kind, effective_at ORDER BY created_at, id) AS n
      FROM terms) d
WHERE d.id = t.id AND d.n > 1;
DROP INDEX terms_kind_idx;
CREATE UNIQUE INDEX terms_kind_effective_uniq ON terms (kind, effective_at DESC);

-- 외래키인데 인덱스가 없던 두 컬럼. 부모 행을 지우거나 키를 바꿀 때마다 자식 표를
-- 순차 탐색했다.
CREATE INDEX returns_new_variant_idx ON returns (new_variant_id) WHERE new_variant_id IS NOT NULL;
CREATE INDEX role_permissions_permission_id_idx ON role_permissions (permission_id);

-- return_items_item_uniq (return_id, order_item_id) 의 앞 컬럼과 같다 — 쓰기 비용만 든다.
DROP INDEX return_items_return_idx;

-- 회원 프로필 값·항목 정의에 posts.custom_fields·board_fields.options 와 같은 모양
-- 제약을 건다. 객체가 아닌 값이 들어가면 템플릿이 그 회원의 프로필을 그리지 못한다.
--
-- **NOT VALID 다.** 지금까지 상한이 없었으므로 이미 넘는 행이 있을 수 있고, 그 한
-- 행 때문에 업그레이드가 부팅에서 멈추면 안 된다. 새로 쓰거나 고치는 행에는 그대로
-- 적용된다.
--
-- user_fields 에는 board_fields_options_when (select·multiselect ⇔ 선택지 있음) 에
-- 해당하는 제약을 **두지 않는다**: 코드(SaveUserField)가 그것을 보장하지 않아, 걸면
-- 지금 저장되는 정의가 제약 위반으로 바뀐다.
ALTER TABLE users ADD CONSTRAINT users_custom_fields_shape
    CHECK (jsonb_typeof(custom_fields) = 'object' AND octet_length(custom_fields::text) <= 16384)
    NOT VALID;
ALTER TABLE user_fields ADD CONSTRAINT user_fields_options_shape
    CHECK (jsonb_typeof(options) = 'array' AND octet_length(options::text) <= 4096)
    NOT VALID;

-- 작업 로그 append-only 의 남은 구멍 (D15 7절): 행 트리거는 TRUNCATE 를 보지 못한다.
-- 함수는 OLD·NEW 를 읽지 않으므로 문장 트리거에 그대로 쓴다.
CREATE TRIGGER operation_logs_no_truncate BEFORE TRUNCATE ON operation_logs
    FOR EACH STATEMENT EXECUTE FUNCTION operation_logs_append_only();

-- 「주문·품목·결제·환불 행은 지워지지 않는다」(D30) 를 RESTRICT 가 지킨 것은 **자식이
-- 있는 부모**뿐이었다: 환불 → 결제 → 품목 → 주문 순으로 지우면 전부 지워졌다 (실측).
-- 애플리케이션에는 이 네 표를 지우는 경로가 없다 — 상태로 닫는다.
-- +goose StatementBegin
CREATE FUNCTION money_rows_no_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% 행은 지울 수 없습니다 (D30 3-1)', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER orders_no_delete BEFORE DELETE ON orders
    FOR EACH ROW EXECUTE FUNCTION money_rows_no_delete();
CREATE TRIGGER order_items_no_delete BEFORE DELETE ON order_items
    FOR EACH ROW EXECUTE FUNCTION money_rows_no_delete();
CREATE TRIGGER payments_no_delete BEFORE DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION money_rows_no_delete();
CREATE TRIGGER refunds_no_delete BEFORE DELETE ON refunds
    FOR EACH ROW EXECUTE FUNCTION money_rows_no_delete();

-- +goose Down

DROP TRIGGER refunds_no_delete ON refunds;
DROP TRIGGER payments_no_delete ON payments;
DROP TRIGGER order_items_no_delete ON order_items;
DROP TRIGGER orders_no_delete ON orders;
DROP FUNCTION money_rows_no_delete();
DROP TRIGGER operation_logs_no_truncate ON operation_logs;
ALTER TABLE user_fields DROP CONSTRAINT user_fields_options_shape;
ALTER TABLE users DROP CONSTRAINT users_custom_fields_shape;
CREATE INDEX return_items_return_idx ON return_items (return_id);
DROP INDEX role_permissions_permission_id_idx;
DROP INDEX returns_new_variant_idx;
-- 시행 시각을 민 것(위 UPDATE)은 되돌리지 않는다 — 어느 행을 밀었는지 남기지 않았다.
DROP INDEX terms_kind_effective_uniq;
CREATE INDEX terms_kind_idx ON terms (kind, effective_at DESC);
-- **실패한 차액 결제가 둘 이상 쌓인 교환 건이 있으면 여기서 멈춘다** (옛 유니크는
-- 실패 행도 센다). 그때는 백업 복원이다 (NFR-308).
DROP INDEX payments_exchange_idx;
CREATE UNIQUE INDEX payments_exchange_idx ON payments (order_id, return_id)
    WHERE kind = '교환차액';
