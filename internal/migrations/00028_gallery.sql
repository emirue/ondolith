-- +goose Up

-- FR-515: 갤러리 목록. 게시판이 목록 모양을 고르고(list·gallery), 글은 카드에 쓸
-- 사진(본문의 첫 업로드 사진, P-213)을 가리킨다.
--
-- 카드 사진을 목록 질의에서 본문 HTML 로부터 뽑지 않는 이유: 목록은 본문을 읽지 않는다
-- (post_shape_test) — 쪽마다 본문 20개를 들고 다니면 목록이 본문 크기만큼 무거워진다.
-- 저장할 때 한 번 정해 둔다.
ALTER TABLE boards ADD COLUMN list_style text NOT NULL DEFAULT 'list'
    CONSTRAINT boards_list_style_known CHECK (list_style IN ('list', 'gallery'));
-- SET NULL: 사진 행이 사라지면 카드가 사진 없이 남는다. 글은 그대로다.
ALTER TABLE posts ADD COLUMN cover_media_id uuid REFERENCES media(id) ON DELETE SET NULL;

-- +goose Down
-- 되돌리면 갤러리 게시판이 목록으로 돌아가고 카드 사진 기록이 사라진다(사진은 남는다).
ALTER TABLE posts DROP COLUMN cover_media_id;
ALTER TABLE boards DROP COLUMN list_style;
