-- +goose Up

-- FR-627: 조합마다 상품 포장에 이미 인쇄된 제조사 바코드를 둔다. 1D 전용 스캐너는
-- A-513 의 QR 라벨을 읽지 못하므로, 바코드가 없으면 그 설치처는 스캔으로 조합을
-- 특정할 수 없다. 형식은 검사하지 않는다 (EAN-13·UPC·Code128 등 체계가 여럿이다).
-- 상한은 SKU 와 같다 (D30 3-2).
ALTER TABLE product_variants ADD COLUMN barcode text
    CHECK (barcode IS NULL OR length(barcode) BETWEEN 1 AND 64);

-- 같은 바코드가 두 조합을 가리키면 스캔이 어느 재고를 움직일지 정할 수 없다.
-- 비어 있는 조합이 대부분이라 부분 인덱스다 (SKU 와 같은 모양).
CREATE UNIQUE INDEX product_variants_barcode_idx ON product_variants (barcode)
    WHERE barcode IS NOT NULL;

-- +goose Down

DROP INDEX product_variants_barcode_idx;
ALTER TABLE product_variants DROP COLUMN barcode;
