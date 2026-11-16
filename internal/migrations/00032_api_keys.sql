-- +goose Up

-- D42 6절 사이트 확장의 앱 키. 모바일 앱이 `Authorization: Bearer <키>` 로 /api/site/ 를 부른다.
--
-- **키 원문은 저장하지 않는다.** 32바이트 난수라 SHA-256 하나로 충분하다(맞히기 공격의 대상이 아니다 — 비밀번호처럼
-- 사람이 고른 값이 아니다). 원문은 발급 화면에서 한 번 보이고 세션·작업 로그·백업 어디에도 남지 않는다.
-- 폐기는 행을 지우지 않고 시각을 적는다 — 작업 로그의 「언제 누가 어떤 키를 폐기했나」가 그 이름을 가리킨다.
CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    key_hash     bytea       NOT NULL UNIQUE CHECK (octet_length(key_hash) = 32),
    created_by   uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- +goose Down

DROP TABLE api_keys;
