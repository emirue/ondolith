-- +goose Up

-- 통합 검색(P-212)이 맞는 글이 많을 때 정렬 순서대로 걸으며 거르다 한 쪽이 차면 멈추게 하는 인덱스
-- (queries/post.sql SearchPosts). 기본 정렬이 「고정 글 → 최신」이라 고정 여부가 앞이어야 하고,
-- posts_recent_idx(최신만, 홈의 최근 글)로는 그 순서를 못 받는다 — 전체를 훑고 정렬했다.
-- 100만 건 실측(2026-10-13): 흔한 낱말 1쪽 957ms → 5ms. 만드는 데 약 1.5초.
CREATE INDEX posts_pub_pinned_recent_idx
    ON posts (is_pinned DESC, created_at DESC, id DESC) WHERE status = 'published';

-- +goose Down
DROP INDEX posts_pub_pinned_recent_idx;
