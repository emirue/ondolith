-- +goose Up

-- FR-513: 리치 텍스트 본문. 에디터가 만든 HTML 은 **정화를 지난 것만** 여기 들어가고
-- (internal/content/richtext.go), 기존 body 는 그 HTML 의 평문이 된다 — 검색
-- (search_vector)·요약·OG 설명이 body 를 그대로 쓰므로 바뀌는 질의가 없다.
--
-- 빈 문자열이 「평문 글」이다. 에디터 이전의 글과 JS 없이 쓴 글은 body_html 이
-- 비어 있고 지금처럼 줄바꿈만 살려 그린다. NULL 을 두지 않는 것은 그 둘을 가르는
-- 값이 하나여야 해서다.
--
-- 상한은 평문 상한(50,000자)의 네 배다. 태그·속성이 글자보다 길다.
ALTER TABLE posts ADD COLUMN body_html text NOT NULL DEFAULT ''
    CONSTRAINT posts_body_html_len CHECK (length(body_html) <= 200000);
ALTER TABLE pages ADD COLUMN body_html text NOT NULL DEFAULT ''
    CONSTRAINT pages_body_html_len CHECK (length(body_html) <= 200000);

-- +goose Down
-- 되돌리면 에디터로 쓴 글은 평문(body)만 남는다 — 서식과 사진 위치가 사라진다.
ALTER TABLE pages DROP COLUMN body_html;
ALTER TABLE posts DROP COLUMN body_html;
