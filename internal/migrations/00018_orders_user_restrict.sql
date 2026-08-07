-- +goose Up

-- `orders.user_id` 는 RESTRICT 여야 한다 (D30 3-1, FR-212).
--
-- 00012 는 `ON DELETE SET NULL` 로 만들었다. 그래서 주문 이력이 있는 계정을
-- 삭제하면 **거부되지 않고 주문의 주인만 조용히 사라졌다.** FR-212 가 삭제를
-- 막는 이유가 "주문 주체가 사라지면 정산·분쟁 대응이 불가능하다" 인데,
-- SET NULL 은 그 상태를 막는 것이 아니라 정확히 만들어 낸다.
--
-- auth.DeleteUser 는 이 제약이 RESTRICT 라고 **믿고** 23503 을 ErrUserInUse 로
-- 옮긴다 — 주석에도 "orders are RESTRICT" 라고 적혀 있었다. 코드·문서·스키마
-- 셋 중 스키마만 달랐고, 그것을 확인하는 테스트가 없어서 아무도 몰랐다.
--
-- 이미 주인을 잃은 주문이 있으면 이 마이그레이션은 **실패하지 않는다** —
-- NULL 은 외래키 검사를 받지 않는다. 그런 행은 비회원 주문으로 남고, 주문번호로
-- 조회할 수 있다 (P-503). 되살릴 방법은 없으므로 되살리지 않는다.

ALTER TABLE orders DROP CONSTRAINT orders_user_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT;

-- +goose Down

-- 되돌리면 「주문 이력이 있는 계정은 지울 수 없다」가 다시 꺼진다.
ALTER TABLE orders DROP CONSTRAINT orders_user_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;
