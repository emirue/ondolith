-- +goose Up

CREATE TABLE carts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        REFERENCES users(id) ON DELETE CASCADE,
    guest_key  text        CHECK (guest_key IS NULL OR length(guest_key) BETWEEN 16 AND 128),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- 회원 장바구니이거나 비회원 장바구니이지 둘 다이거나 둘 다 아닐 수는 없다.
    CONSTRAINT carts_owner_is_one CHECK ((user_id IS NULL) <> (guest_key IS NULL))
);

CREATE UNIQUE INDEX carts_user_idx  ON carts (user_id)   WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX carts_guest_idx ON carts (guest_key) WHERE guest_key IS NOT NULL;

CREATE TABLE cart_items (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    cart_id    uuid        NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    -- CASCADE 다. **장바구니는 이력이 아니다** — 금액도 동의도 담지 않으므로
    -- 조합이 사라지면 항목도 사라진다. RESTRICT 로 두면 익명 방문자가 담기만
    -- 해도 관리자의 조합 삭제를 막는다.
    variant_id uuid        NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
    quantity   integer     NOT NULL CHECK (quantity >= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- 같은 조합은 한 행이고 수량이 는다 (D50 「Phase 3 정책값」: 수량 합산).
    CONSTRAINT cart_items_variant_uniq UNIQUE (cart_id, variant_id)
);

CREATE INDEX cart_items_variant_idx ON cart_items (variant_id);

CREATE TABLE orders (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 순번이 아니다. 순번이면 주문번호 하나로 남의 주문을 훑을 수 있고,
    -- 비회원 조회(P-504)가 그 번호를 입력으로 받는다 (SC-3 3항).
    order_no       text        NOT NULL UNIQUE CHECK (length(order_no) BETWEEN 6 AND 32),
    user_id        uuid        REFERENCES users(id) ON DELETE SET NULL,
    status         text        NOT NULL DEFAULT '결제대기',
    -- P-408 금액 대조의 단일 출처. 승인 요청 금액이 이것과 다르면 거부한다.
    total_amount   integer     NOT NULL CHECK (total_amount >= 0),
    receiver_name  text        NOT NULL CHECK (length(receiver_name) BETWEEN 1 AND 100),
    receiver_phone text        NOT NULL CHECK (length(receiver_phone) BETWEEN 1 AND 20),
    postcode       text        NOT NULL CHECK (length(postcode) BETWEEN 1 AND 10),
    address1       text        NOT NULL CHECK (length(address1) BETWEEN 1 AND 200),
    address2       text        NOT NULL DEFAULT '' CHECK (length(address2) <= 200),
    delivery_memo  text        NOT NULL DEFAULT '' CHECK (length(delivery_memo) <= 200),
    -- 회원도 세션 이메일을 복사한다. 계정이 지워져도(SET NULL) 주문서를 보낼
    -- 곳이 남아야 한다.
    orderer_email  text        NOT NULL CHECK (length(orderer_email) BETWEEN 3 AND 254),
    -- NOT NULL 이다. 원래 "비회원만 필수" 였는데, user_id 가 SET NULL 이라
    -- 회원 계정을 지우는 순간 user_id 와 orderer_phone 이 **둘 다 NULL** 이 되어
    -- 아래 CHECK 가 깨지고 사용자 삭제 자체가 막혔다 — 통합 테스트가 잡았다.
    --
    -- 고치는 방향은 CHECK 를 푸는 것이 아니라 연락처를 항상 받는 것이다.
    -- 배송이 있는 주문에 전화번호가 없는 경우는 없고, 그것이 계정과 무관하게
    -- 남아야 주문이 계속 열린다 (이메일 스냅샷과 같은 이유).
    orderer_phone  text        NOT NULL CHECK (length(orderer_phone) BETWEEN 1 AND 20),
    -- A-512 의 반품 기간(7일)·자동 확정(8일)이 전부 이 시각 기준이다. 없으면
    -- operation_logs 를 운영 판정에 쓰게 되는데, 그 표는 감사 흔적이지 운영
    -- 데이터가 아니다.
    delivered_at   timestamptz,
    confirmed_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    -- 16개. D14 5절이 단일 출처다.
    CONSTRAINT orders_status_known CHECK (status IN (
        '결제대기','입금대기','결제완료','결제실패',
        '배송준비','배송중','배송완료','구매확정',
        '취소','환불',
        '반품접수','반품수거',
        '교환접수','교환수거','차액결제대기','교환발송')),
    -- orderer_phone 이 NOT NULL 이 되면서 "둘 중 하나는 있다" 를 따로 적을
    -- 필요가 없어졌다. 전화번호는 비회원 조회(P-504)의 대조 키이기도 하다.
    CONSTRAINT orders_email_present CHECK (orderer_email <> '')
);

CREATE INDEX orders_user_idx      ON orders (user_id, created_at DESC);
CREATE INDEX orders_status_idx    ON orders (status, created_at DESC);
CREATE INDEX orders_delivered_idx ON orders (delivered_at) WHERE status = '배송완료';

CREATE TABLE order_items (
    id         uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    -- RESTRICT: **주문 행은 지우지 않는다.** DB 가 막는다.
    order_id   uuid    NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    -- RESTRICT: 주문된 상품·조합은 물리 삭제 불가. 이것이 products 에 소프트
    -- 삭제 컬럼을 두지 않는 근거다.
    product_id uuid    NOT NULL REFERENCES products(id)         ON DELETE RESTRICT,
    variant_id uuid    NOT NULL REFERENCES product_variants(id) ON DELETE RESTRICT,

    -- 아래 넷은 **스냅샷**이다. FR-612 가 "스냅샷만으로 주문서 재발행"을
    -- 요구하므로 FK 조인으로 대체하지 않는다 — 이름이 바뀌거나 조합이 은퇴한
    -- 뒤에도 그때 산 것이 그대로 재현돼야 한다.
    product_name text  NOT NULL CHECK (length(product_name) BETWEEN 1 AND 200),
    -- option_label 이 없으면 조합이 은퇴·변경된 뒤 주문서가 옵션을 재현하지
    -- 못한다. "색상: 검정 / 사이즈: L" 형태.
    option_label text  NOT NULL DEFAULT '' CHECK (length(option_label) <= 200),
    unit_price integer NOT NULL CHECK (unit_price >= 0),
    quantity   integer NOT NULL CHECK (quantity >= 1),
    -- 생성 컬럼이라 품목 금액이 단가·수량과 어긋날 수 없다.
    line_amount integer GENERATED ALWAYS AS (unit_price * quantity) STORED,
    -- 잔여 수량을 매번 합산으로 구하면 동시 요청 두 건이 함께 통과한다.
    -- refunded_amount 와 같은 패턴이다.
    settled_quantity integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT order_items_settled_range CHECK (settled_quantity BETWEEN 0 AND quantity)
);

CREATE INDEX order_items_order_idx   ON order_items (order_id);
CREATE INDEX order_items_product_idx ON order_items (product_id);
CREATE INDEX order_items_variant_idx ON order_items (variant_id);

CREATE TABLE terms (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 허용목록을 두지 않는다. 약관 종류는 판매 형태마다 다르고, 목록을 두면
    -- 종류를 하나 늘릴 때 마이그레이션이 필요하다.
    kind         text        NOT NULL CHECK (length(kind) BETWEEN 1 AND 50),
    version      text        NOT NULL CHECK (length(version) BETWEEN 1 AND 20),
    -- 평문이다 (D50 「Phase 3 정책값」). 정화 라이브러리를 하나 더 유지하는
    -- 비용이 약관에 표를 넣는 가치보다 크다.
    body         text        NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    effective_at timestamptz NOT NULL,
    is_required  boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT terms_kind_version_uniq UNIQUE (kind, version),
    -- 소급 금지. 소급이 되면 "주문 시점에 유효했던 약관"이 나중에 바뀔 수 있고,
    -- FR-619 가 기록하는 동의 버전이 재현을 보장하지 못한다.
    CONSTRAINT terms_no_backdate CHECK (effective_at >= created_at)
);

CREATE INDEX terms_kind_idx ON terms (kind, effective_at DESC);

-- terms 에 updated_at 이 없다 (D30 3절 예외) — 배포된 버전은 수정하지 않고
-- 개정은 새 행이다. 그래서 order_agreements 가 본문을 복사하지 않아도 된다.

CREATE TABLE order_agreements (
    order_id uuid        NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    -- RESTRICT: 동의 이력이 가리키는 약관이 사라지면 이력이 거짓이 된다.
    terms_id uuid        NOT NULL REFERENCES terms(id)  ON DELETE RESTRICT,
    agreed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (order_id, terms_id)
);

CREATE INDEX order_agreements_terms_idx ON order_agreements (terms_id);

-- 약관 본문을 복사하지 않는다 — terms 행이 불변이고 terms_id RESTRICT 가
-- 삭제를 막으므로 참조만으로 FR-619 의 "나중에 재현된다"가 성립한다. 복사하면
-- 주문 수만큼 본문이 복제된다.

-- +goose Down

DROP TABLE order_agreements;
DROP TABLE terms;
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE cart_items;
DROP TABLE carts;
