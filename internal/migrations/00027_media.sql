-- +goose Up

-- FR-514: 에디터로 올린 본문 사진. 첨부(attachments)와 달리 **글보다 먼저** 생긴다 —
-- 글을 저장하기 전에 사진을 넣으므로 부모가 아직 없다. 그래서 post_id 를 갖지 않고,
-- 본문 HTML 이 `/media/{id}` 로 가리킨다.
--
-- 파일은 업로드 루트의 `media/YYYY/MM/<uuid>`(본문용)와 `…-thumb`(썸네일) 둘이다.
-- 원본은 남기지 않는다: 다시 인코딩한 것만 저장해 EXIF(위치정보)가 디스크에도 없다.
-- ponytail: 글을 저장하지 않고 떠난 사진은 남는다 — 지우는 경로는 디스크가 문제가 될 때.
CREATE TABLE media (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SET NULL: 올린 사람이 사라져도 글 속 사진은 남는다 (글의 author_id 와 같다).
    uploader_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    stored_path text        NOT NULL UNIQUE
                            CHECK (stored_path ~ '^media/[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$'),
    -- 다시 인코딩한 결과의 타입이다. webp 는 JPEG 가 되어 여기 없다(인코더가 없다).
    mime_type   text        NOT NULL CHECK (mime_type IN ('image/jpeg', 'image/png', 'image/gif')),
    thumb_mime  text        NOT NULL CHECK (thumb_mime IN ('image/jpeg', 'image/png')),
    width       integer     NOT NULL CHECK (width > 0),
    height      integer     NOT NULL CHECK (height > 0),
    byte_size   bigint      NOT NULL CHECK (byte_size > 0),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- 되돌리면 본문의 `/media/…` 사진이 404 가 된다. 파일은 디스크에 남는다.
DROP TABLE media;
