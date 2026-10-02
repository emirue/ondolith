-- +goose Up

-- FR-601: 상품 이미지. 첨부(attachments)와 같은 모양이다 — 파일은 업로드 루트에
-- `YYYY/MM/<uuid>` 로 저장되고(D60 3항), 이 표는 그 경로와 서버가 산출한 타입·크기를
-- 갖는다. 표시용 원본 파일명은 디스크에 닿지 않는다.
--
-- 별도 표인 이유: 상품 하나에 이미지가 여럿이고, attachments.post_id 를 NULL 허용으로
-- 풀어 같이 쓰면 「부모 글의 읽기 권한을 다시 검사한다」(D15 SC-7 2항)의 부모가
-- 행마다 달라진다.
CREATE TABLE product_images (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- CASCADE: 상품이 사라지면 그 이미지 행도 간다. 파일은 따라가지 않으므로
    -- 상품 삭제 경로가 같은 요청에서 지운다 (첨부와 같은 규칙).
    product_id    uuid        NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    stored_path   text        NOT NULL UNIQUE
                              CHECK (stored_path ~ '^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$'
                                     AND length(stored_path) <= 128),
    original_name text        NOT NULL CHECK (length(original_name) BETWEEN 1 AND 255),
    -- **래스터 이미지 넷뿐이다.** 이 표의 파일은 `Content-Disposition: inline` 으로
    -- 나가므로(<img> 가 그린다), 브라우저가 문서로 해석할 타입이 들어올 수 없다는
    -- 것을 업로드 검증기와 따로 DB 가 보장한다. SVG 는 없다 (D60).
    mime_type     text        NOT NULL
                              CHECK (mime_type IN ('image/jpeg', 'image/png', 'image/gif', 'image/webp')),
    byte_size     bigint      NOT NULL CHECK (byte_size > 0),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- 순서는 올린 순서다. 첫 행이 목록의 대표 이미지이고, 이 인덱스가 그 한 행을 준다.
-- sort_order 컬럼은 두지 않는다 — 순서를 바꾸는 화면이 없다.
CREATE INDEX product_images_product_idx ON product_images (product_id, created_at, id);

-- updated_at 을 두지 않는다 (D30 3절 예외): 생성과 삭제만 있다.

-- +goose Down

-- 행만 지운다. 업로드 루트의 파일은 남는다 — 다운그레이드한 바이너리는 그 파일을
-- 읽지 않으므로 해가 없고, 다시 올리면 새 이름으로 저장된다.
DROP TABLE product_images;
