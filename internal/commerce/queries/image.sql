-- 상품 이미지 (image.go, A-502 · P-306). 파일은 업로드 루트에 있고 이 표는 경로를 갖는다.

-- name: CountProductImages :one
SELECT count(*) FROM product_images WHERE product_id = $1;

-- name: CreateProductImage :one
INSERT INTO product_images (product_id, stored_path, original_name, mime_type, byte_size)
VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at;

-- 올린 순서다. 첫 행이 대표 이미지다 (ListProducts 의 image_id 와 같은 정렬).
-- name: ProductImages :many
SELECT id, product_id, stored_path, original_name, mime_type, byte_size, created_at
FROM product_images WHERE product_id = $1 ORDER BY created_at, id;

-- 상품의 노출 여부를 **같은 문장으로** 낸다. 따로 읽으면 한쪽만 확인하는 호출자가 생긴다.
-- name: ProductImageByID :one
SELECT i.id, i.product_id, i.stored_path, i.original_name, i.mime_type, i.byte_size, i.created_at,
       p.is_visible AS product_visible
FROM product_images i JOIN products p ON p.id = i.product_id
WHERE i.id = $1;

-- 지우면서 경로를 받는다. 읽고 나서 지우면 그 사이에 행이 사라질 수 있다.
-- name: DeleteProductImage :one
DELETE FROM product_images WHERE id = $1 RETURNING product_id, stored_path;

-- name: ProductImagePaths :many
SELECT stored_path FROM product_images WHERE product_id = $1;
