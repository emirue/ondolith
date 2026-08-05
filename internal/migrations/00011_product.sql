-- +goose Up

-- 커머스 스키마는 site_mode 와 무관하게 **항상** 만든다 (D20 모듈 게이팅).
-- 조건부 스키마는 설치처마다 다른 DB 를 낳고, 그러면 마이그레이션이 "어느
-- 설치처인가"를 물어야 한다. shop 이 아니면 라우트가 등록되지 않을 뿐이다.

CREATE TABLE products (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text        NOT NULL UNIQUE
                            CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$' AND length(slug) <= 100),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text        NOT NULL DEFAULT '' CHECK (length(description) <= 20000),
    -- 금액은 정수 minor unit 이다. 부동소수는 어디선가 반올림되고, 그 어디선가는
    -- 매번 다른 곳이다 (D30 「금액」).
    base_price  integer     NOT NULL CHECK (base_price >= 0),
    -- 기본값이 false 인 것은 fail-closed 다. 옵션과 재고를 넣기 전에 팔리는 것을
    -- 막는다 — A-503 이 뒤에 온다.
    is_visible  boolean     NOT NULL DEFAULT false,
    -- 게시판 검색과 같은 이유로 simple config + 2인자 형태다 (D30 Phase 2 측정).
    search_tsv  tsvector    GENERATED ALWAYS AS
                            (to_tsvector('simple'::regconfig, name || ' ' || description)) STORED,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX products_visible_idx ON products (is_visible, created_at DESC);
CREATE INDEX products_search_idx  ON products USING GIN (search_tsv);

-- 소프트 삭제 컬럼을 두지 않는다. order_items.product_id 의 RESTRICT 가 물리
-- 삭제를 DB 에서 막으므로, "안 보이는 상태"를 둘로 만들 이유가 없다.

CREATE TABLE product_options (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id uuid        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    values     jsonb       NOT NULL,
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_options_name_uniq UNIQUE (product_id, name),
    CONSTRAINT product_options_values_shape
        CHECK (jsonb_typeof(values) = 'array' AND jsonb_array_length(values) BETWEEN 1 AND 50)
);

CREATE TABLE product_variants (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id  uuid        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    -- 키는 옵션 그룹 **이름 문자열**이지 product_option_id 가 아니다. JSONB 에는
    -- FK 를 걸 수 없어 ID 를 키로 쓰면 그룹 삭제가 고아 참조를 만들고, 은퇴한
    -- 조합을 주문 이력이 계속 가리킨다. 대가는 그룹명을 바꿀 때 같은 트랜잭션
    -- 에서 조합 키도 갱신해야 한다는 것이다.
    option_values jsonb     NOT NULL,
    sku         text        CHECK (sku IS NULL OR length(sku) BETWEEN 1 AND 64),
    -- 음수를 허용한다. 낮은 등급 옵션이 기본가보다 싼 것이 정상이고, 금지하면
    -- 기본가를 최저 조합에 맞추는 우회가 생겨 표시 가격이 거짓이 된다.
    price_delta integer     NOT NULL DEFAULT 0,
    -- 재고는 delta 갱신(stock = stock + $1)만 한다. 절대값 UPDATE 경로를 만들지
    -- 않는 이유는 delta 두 건이 순서와 무관하게 둘 다 맞기 때문이다.
    stock       integer     NOT NULL DEFAULT 0 CHECK (stock >= 0),
    is_visible  boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT product_variants_combo_uniq UNIQUE (product_id, option_values),
    CONSTRAINT product_variants_option_values_shape
        CHECK (jsonb_typeof(option_values) = 'object'
               AND octet_length(option_values::text) <= 4096)
);

-- WHERE 절은 규칙이 아니라 색인 크기다. NULL 은 기본 규칙에서 서로 같지 않으므로
-- 평범한 UNIQUE 도 SKU 없는 조합을 여럿 허용한다 — 이 부분 인덱스를 통째 UNIQUE
-- 로 바꿔도 동작은 같고, 다만 SKU 없는 행들이 색인에 들어간다. 조합의 대부분이
-- SKU 를 갖지 않는 것이 정상이라 그 차이가 작지 않다.
CREATE UNIQUE INDEX product_variants_sku_idx ON product_variants (sku) WHERE sku IS NOT NULL;
-- 판매 가능한 조합만 색인한다. 목록 화면이 묻는 것이 그것이고, 은퇴·품절 조합은
-- 그 질문의 답이 아니다.
CREATE INDEX product_variants_sellable_idx ON product_variants (product_id)
    WHERE is_visible AND stock > 0;

CREATE TABLE categories (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- RESTRICT: 하위가 있는 카테고리는 삭제를 거부한다. CASCADE 면 최상위 하나를
    -- 지우는 실수가 분류 체계 전체를 지운다.
    parent_id  uuid        REFERENCES categories(id) ON DELETE RESTRICT,
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- 전역 UNIQUE 다. 경로가 아니라 이름 하나로 URL 을 만들기 때문이고, 부모별
    -- UNIQUE 면 /category/{slug} 가 어느 것인지 정해지지 않는다.
    slug       text        NOT NULL UNIQUE
                           CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$' AND length(slug) <= 100),
    sort_order integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT categories_no_self_parent CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE INDEX categories_parent_idx ON categories (parent_id, sort_order)
    WHERE parent_id IS NOT NULL;

-- depth 컬럼을 두지 않는다. 안전 상한 10 은 설계 제약이 아니라 폭주 방지턱이고
-- (A-509), depth 를 물리화하면 서브트리를 옮길 때마다 갱신 코드가 붙는다.
-- 순환·깊이는 재귀 CTE 검사 + pg_advisory_xact_lock 직렬화로 간다.

CREATE TABLE product_categories (
    product_id  uuid        NOT NULL REFERENCES products(id)   ON DELETE CASCADE,
    -- RESTRICT: 소속 상품이 있는 카테고리는 삭제 거부.
    category_id uuid        NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (product_id, category_id)
);

-- FK 인덱스 규칙: PK 의 선두가 product_id 라 그쪽은 덮이고, category_id 만 따로.
CREATE INDEX product_categories_category_idx ON product_categories (category_id);

-- product_categories 에 updated_at 을 두지 않는다 (D30 3절 예외) — 갱신하지 않는
-- 순수 연결 표이고, 분류를 바꾸는 것은 행을 지우고 넣는 일이다.

-- +goose Down

DROP TABLE product_categories;
DROP TABLE categories;
DROP TABLE product_variants;
DROP TABLE product_options;
DROP TABLE products;
