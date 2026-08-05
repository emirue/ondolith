-- +goose Up

CREATE TABLE returns (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 주문번호와 같은 이유로 순번이 아니다 (SC-3 3항).
    return_no  text        NOT NULL UNIQUE CHECK (length(return_no) BETWEEN 6 AND 32),
    order_id   uuid        NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    kind       text        NOT NULL,
    -- 상태 값은 D14 의 주문 상태 라벨을 그대로 쓴다. 한 주문에 서로 다른 품목의
    -- 반품·교환이 동시에 진행될 수 있어 orders.status 하나로는 건별 단계를
    -- 표현하지 못하는데, 여기서 새 이름을 만들면 같은 개념에 두 어휘가 생긴다.
    status     text        NOT NULL,
    reason        text     NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    reject_reason text     NOT NULL DEFAULT '' CHECK (length(reject_reason) <= 500),
    -- 수거 확인 시 확정된다. 그전에는 아직 모르는 값이라 NULL 이다.
    fault      text,
    -- A-512 의 배송비 정책·금액 **스냅샷**. 참조가 아니라 복사인 이유는 정책을
    -- 바꾸는 것만으로 과거 환불액이 달라지면 안 되기 때문이다.
    shipping_fee_policy text,
    shipping_fee_amount integer,
    new_variant_id uuid    REFERENCES product_variants(id) ON DELETE RESTRICT,
    -- 부호 있음. 새 조합이 더 싸면 음수이고, 그때는 차액 결제가 아니라 환불이다.
    price_difference integer,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT returns_kind_known CHECK (kind IN ('반품','교환')),
    -- 종류와 상태가 짝이 맞는다. 교환 건이 '환불' 상태로 가는 경로는 없다.
    CONSTRAINT returns_status_matches_kind CHECK (
        (kind = '반품' AND status IN ('반품접수','반품수거','환불','거부'))
     OR (kind = '교환' AND status IN ('교환접수','교환수거','차액결제대기','교환발송','거부'))),
    CONSTRAINT returns_fault_known CHECK (fault IS NULL OR fault IN ('구매자','판매자')),
    CONSTRAINT returns_fee_policy_known
        CHECK (shipping_fee_policy IS NULL OR shipping_fee_policy IN ('차감','별도청구')),
    CONSTRAINT returns_fee_amount_range
        CHECK (shipping_fee_amount IS NULL OR shipping_fee_amount >= 0),
    CONSTRAINT returns_exchange_only_fields
        CHECK (kind = '교환' OR (new_variant_id IS NULL AND price_difference IS NULL)),
    CONSTRAINT returns_exchange_needs_variant
        CHECK (kind <> '교환' OR new_variant_id IS NOT NULL),
    -- 차액을 받으러 가는 상태인데 받을 차액이 없거나 음수면 P-514 가 0원 결제를
    -- 만든다.
    CONSTRAINT returns_diff_positive_when_pending
        CHECK (status <> '차액결제대기' OR price_difference > 0),
    -- 수거 확인 = 스냅샷 복사. 이 시점을 넘겼는데 스냅샷이 비어 있으면 환불액이
    -- 나중의 A-512 값으로 계산된다.
    CONSTRAINT returns_snapshot_after_pickup CHECK (
        kind <> '반품' OR status NOT IN ('반품수거','환불')
        OR (fault IS NOT NULL AND shipping_fee_policy IS NOT NULL
            AND shipping_fee_amount IS NOT NULL)),
    -- 하자 상품의 반품비를 구매자가 물지 않는다.
    CONSTRAINT returns_seller_fault_free CHECK (fault <> '판매자' OR shipping_fee_amount = 0)
);

CREATE INDEX returns_order_idx  ON returns (order_id, created_at DESC);
CREATE INDEX returns_status_idx ON returns (status, created_at DESC);

CREATE TABLE return_items (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    return_id     uuid        NOT NULL REFERENCES returns(id)     ON DELETE CASCADE,
    order_item_id uuid        NOT NULL REFERENCES order_items(id) ON DELETE RESTRICT,
    quantity      integer     NOT NULL CHECK (quantity >= 1),
    -- 비정규화이고 이유가 하나뿐이다: PostgreSQL 부분 인덱스의 술어는 **같은
    -- 테이블의 컬럼만** 참조할 수 있어 returns.status 를 볼 수 없다. "같은 품목에
    -- 처리 중인 건이 둘 이상 생기지 않는다" 를 DB 로 강제하려면 상태를 내려야 한다.
    -- returns 가 종결로 전이하는 트랜잭션에서 함께 false 로 내린다.
    is_open       boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT return_items_item_uniq UNIQUE (return_id, order_item_id)
);

-- 같은 물건을 두 번 환불받지 못한다.
CREATE UNIQUE INDEX return_items_open_idx ON return_items (order_item_id) WHERE is_open;
CREATE INDEX return_items_return_idx ON return_items (return_id);

CREATE TABLE shipments (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- order_id 에 UNIQUE 를 걸지 않는다. 걸면 D14 의 `교환발송 → 배송완료` 복귀
    -- 흐름이 성립하지 않는다 — 아래 부분 유니크 둘이 실제로 지키려던 불변식이다.
    order_id    uuid        NOT NULL REFERENCES orders(id)  ON DELETE RESTRICT,
    return_id   uuid        REFERENCES returns(id) ON DELETE RESTRICT,
    kind        text        NOT NULL,
    carrier     text        NOT NULL CHECK (length(carrier) BETWEEN 1 AND 32),
    tracking_no text        NOT NULL CHECK (length(tracking_no) BETWEEN 1 AND 64),
    shipped_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT shipments_kind_known CHECK (kind IN ('최초발송','교환재발송')),
    CONSTRAINT shipments_exchange_has_return CHECK ((kind = '교환재발송') = (return_id IS NOT NULL))
);

CREATE UNIQUE INDEX shipments_first_idx  ON shipments (order_id)  WHERE kind = '최초발송';
CREATE UNIQUE INDEX shipments_return_idx ON shipments (return_id) WHERE return_id IS NOT NULL;

-- payments·refunds 의 return_id FK 는 여기서 건다. returns 가 이 마이그레이션에서
-- 생기기 때문이고, W3-05 에서 컬럼만 만들어 두었다 (D30 「payments」 주석).
ALTER TABLE payments ADD CONSTRAINT payments_return_fk
    FOREIGN KEY (return_id) REFERENCES returns(id) ON DELETE RESTRICT;
ALTER TABLE refunds  ADD CONSTRAINT refunds_return_fk
    FOREIGN KEY (return_id) REFERENCES returns(id) ON DELETE RESTRICT;

-- +goose Down

ALTER TABLE refunds  DROP CONSTRAINT refunds_return_fk;
ALTER TABLE payments DROP CONSTRAINT payments_return_fk;
DROP TABLE shipments;
DROP TABLE return_items;
DROP TABLE returns;
