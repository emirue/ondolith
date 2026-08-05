-- +goose Up

-- 돈이 지나가는 표다. 여기 걸린 제약은 전부 D30 「돈·재고 불변식을 DB가 강제하는
-- 목록」의 항목이고, 애플리케이션 검사로 대체하지 않는다 — 승인 콜백과 환불 요청은
-- 동시에 두 번 오는 것이 정상 동작이라 "읽고 판단하고 쓴다"가 항상 진다.

CREATE TABLE payments (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   uuid        NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    -- returns 는 다음 마이그레이션(00014)에서 생긴다. FK 는 거기서 ALTER 로 건다 —
    -- 표 생성 순서를 뒤집으면 returns 가 payments 를 가리키는 것이 아니라서
    -- 순환은 아니지만, W3-05·W3-06 의 순서가 D81 에 그렇게 적혀 있다.
    return_id  uuid,
    kind       text        NOT NULL,
    -- '대기' 는 "결과 불명" 을 포함한다. D50 이 타임아웃에 재승인 시도를 금지하고
    -- 조회 API 로 가라고 했으므로, 그 대상 집합과 A-508 대사 대상이 같아진다.
    status     text        NOT NULL DEFAULT '대기',
    pg          text       NOT NULL CHECK (length(pg) BETWEEN 1 AND 32),
    payment_key text       NOT NULL CHECK (length(payment_key) BETWEEN 1 AND 200),
    approved_amount integer NOT NULL CHECK (approved_amount >= 0),
    -- 요청 시점에 올라가는 **선점액**이다. '거부' 로 전이할 때 같은 트랜잭션에서
    -- 되돌린다 — 이 정의가 없으면 이름이 "이미 나간 돈" 으로 읽혀 이중 계상된다.
    refunded_amount integer NOT NULL DEFAULT 0,
    -- 카드번호·유효기간·CVC 는 컬럼도 없고 여기에도 넣지 않는다 (DEC-3.7, PCI DSS).
    -- 어댑터가 마스킹한 뒤 넣는다.
    raw_response jsonb,
    approved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT payments_kind_known   CHECK (kind   IN ('주문결제','교환차액')),
    CONSTRAINT payments_status_known CHECK (status IN ('대기','승인','실패')),
    -- 이것이 없으면 교환차액 행이 return_id NULL 로 들어와 아래 부분 유니크를
    -- 통째로 우회한다.
    CONSTRAINT payments_exchange_has_return CHECK ((kind = '교환차액') = (return_id IS NOT NULL)),
    -- FR-611. 결제액보다 많은 돈이 나가는 것을 DB 가 막는다.
    CONSTRAINT payments_refund_within_approved
        CHECK (refunded_amount >= 0 AND refunded_amount <= approved_amount)
);

-- FR-608: 주문당 승인 1건. `AND status <> '실패'` 가 붙는 이유는 그것 없이는 승인 API
-- 가 실패한 뒤 행이 영구히 남아 **재결제가 불가능**해지기 때문이다 — P-409 는 "주문은
-- 결제대기에 머문다, 재시도 경로를 남기기 위해서" 라고 못박았다. 동시 두 건은 둘 다
-- '대기' 로 들어가려다 하나만 성공하므로 FR-608 은 그대로 성립한다.
CREATE UNIQUE INDEX payments_order_approved_idx ON payments (order_id)
    WHERE kind = '주문결제' AND status <> '실패';
-- FR-618: 교환 건당 차액 1건.
CREATE UNIQUE INDEX payments_exchange_idx ON payments (order_id, return_id)
    WHERE kind = '교환차액';
-- 같은 승인이 두 행으로 기록되면 A-508 이 무엇이 진짜인지 판정하지 못한다.
CREATE UNIQUE INDEX payments_pg_key_idx ON payments (pg, payment_key);
-- A-508 대사가 읽는 집합.
CREATE INDEX payments_pending_idx ON payments (status, created_at) WHERE status = '대기';
CREATE INDEX payments_return_idx  ON payments (return_id) WHERE return_id IS NOT NULL;

CREATE TABLE refunds (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   uuid        NOT NULL REFERENCES orders(id)   ON DELETE RESTRICT,
    payment_id uuid        NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
    return_id  uuid,
    status     text        NOT NULL DEFAULT '요청',
    requester  text        NOT NULL,
    amount     integer     NOT NULL CHECK (amount > 0),
    reason     text        NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    -- A-507 전용이 아니라 **모든 경로에 NOT NULL** 이다. P-506·P-507 의 중복 제출도
    -- 같은 사고(이중 환불)를 내는데, 화면마다 멱등 수단이 다르면 한쪽만 고쳐진다.
    request_key text       NOT NULL CHECK (length(request_key) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT refunds_status_known    CHECK (status    IN ('요청','승인','거부','완료')),
    CONSTRAINT refunds_requester_known CHECK (requester IN ('구매자','관리자')),
    -- 새로고침 한 번이 이중 환불이 되지 않는다.
    CONSTRAINT refunds_request_key_uniq UNIQUE (request_key)
);

CREATE INDEX refunds_order_idx   ON refunds (order_id, created_at DESC);
CREATE INDEX refunds_payment_idx ON refunds (payment_id);
CREATE INDEX refunds_return_idx  ON refunds (return_id) WHERE return_id IS NOT NULL;

CREATE TABLE refund_items (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- CASCADE: 환불 건에 종속이다. 건이 사라지면 품목도 의미가 없다.
    refund_id     uuid        NOT NULL REFERENCES refunds(id)     ON DELETE CASCADE,
    order_item_id uuid        NOT NULL REFERENCES order_items(id) ON DELETE RESTRICT,
    quantity      integer     NOT NULL CHECK (quantity >= 1),
    created_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT refund_items_item_uniq UNIQUE (refund_id, order_item_id)
);

CREATE INDEX refund_items_order_item_idx ON refund_items (order_item_id);

-- refund_items 에 updated_at 을 두지 않는다 (D30 3절 예외) — 수량을 고치는 일이
-- 없다. 잘못 넣었으면 건을 거부하고 다시 만든다.

CREATE TABLE webhook_events (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    pg         text        NOT NULL CHECK (length(pg) BETWEEN 1 AND 32),
    event_id   text        NOT NULL CHECK (length(event_id) BETWEEN 1 AND 200),
    order_id   uuid        REFERENCES orders(id) ON DELETE RESTRICT,
    -- '수신' 은 "미처리" 를 뜻한다. 고루틴 처리 중 프로세스가 죽으면 반드시 남는
    -- 상태이고, D50 이 자동 재처리를 두지 않기로 했으므로 A-603 이 사람에게 보인다.
    status     text        NOT NULL DEFAULT '수신',
    payload    jsonb       NOT NULL,
    error      text        CHECK (error IS NULL OR length(error) <= 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT webhook_events_status_known CHECK (status IN ('수신','처리완료','실패'))
);

-- FR-610. 복합인 이유: 어댑터가 여럿이라는 것이 FR-605 의 전제이고, 두 PG 가 같은 ID
-- 문자열을 발급하면 단일 컬럼 UNIQUE 는 **정상 이벤트를 중복으로 버린다.**
CREATE UNIQUE INDEX webhook_events_pg_event_idx ON webhook_events (pg, event_id);
-- A-603 상단에 올라가는 집합.
CREATE INDEX webhook_events_unhandled_idx ON webhook_events (status, created_at DESC)
    WHERE status <> '처리완료';
CREATE INDEX webhook_events_order_idx ON webhook_events (order_id) WHERE order_id IS NOT NULL;

-- +goose Down

DROP TABLE webhook_events;
DROP TABLE refund_items;
DROP TABLE refunds;
DROP TABLE payments;
