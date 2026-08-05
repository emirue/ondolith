-- +goose Up

-- 주문 할인 (FR-626) 과 품목 단위 부분 취소 (FR-625).

-- 주문 단위 할인액. 클라이언트가 보내는 값이 아니라 서버가 정한 값이 들어온다
-- (FR-607 — 폼에 금액 필드가 없다는 원칙은 할인에도 그대로다).
ALTER TABLE orders ADD COLUMN discount_amount integer NOT NULL DEFAULT 0
    CHECK (discount_amount >= 0);

-- **배분된 할인의 스냅샷.** 환불할 때마다 비례 계산을 다시 하면 반올림이 매번
-- 달라져 마지막 품목에서 합이 안 맞는다 — 한 번 배분해 두면 부분 취소가
-- 뺄셈이 되고, 전 품목을 취소했을 때 환불 총액이 결제액과 정확히 같아진다.
--
-- 할인이 품목 금액을 넘을 수 없다. 넘으면 그 품목의 환불액이 음수가 되고,
-- 음수 환불은 "돈을 더 받는다" 는 뜻이다.
ALTER TABLE order_items ADD COLUMN discount_amount integer NOT NULL DEFAULT 0
    CHECK (discount_amount >= 0);
ALTER TABLE order_items ADD CONSTRAINT order_items_discount_within_line
    CHECK (discount_amount <= unit_price * quantity);

-- +goose Down

ALTER TABLE order_items DROP CONSTRAINT order_items_discount_within_line;
ALTER TABLE order_items DROP COLUMN discount_amount;
ALTER TABLE orders DROP COLUMN discount_amount;
