-- +goose Up

-- D42 8절 사이트 확장의 문자 인증 로그인. 인부가 앱에서 전화번호로 인증번호를 받아 앱 토큰을 얻는다.
--
-- **인증번호는 번호마다 마지막 하나만 둔다.** 새로 요청하면 덮어쓰고 틀린 횟수도 0 이 된다 — 다시 요청하는 길은 문자
-- 상한(번호당 10분 3통·하루 10통)이 막는다. 6자리라 해시는 맞히기를 막지 못한다(10^6) — DB 를 읽은 사람이 5분 안에
-- 쓸 수 있는 값을 평문으로 두지 않는 정도다. 맞히기는 5분·5회가 막는다.
CREATE TABLE app_login_codes (
    phone      text        PRIMARY KEY CHECK (phone ~ '^01[0-9]{8,9}$'),
    code_hash  bytea       NOT NULL CHECK (octet_length(code_hash) = 32),
    expires_at timestamptz NOT NULL,
    attempts   integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);

-- 앱 토큰. **원문은 저장하지 않는다** — 32바이트 난수라 SHA-256 하나로 충분하다(api_keys 와 같다). subject 는 사이트가
-- 정한 대상(인부 id 등)이다 — 코어는 뜻을 모른다. 그만둔 사람은 사이트가 site.EndSessions 로 행을 지운다.
CREATE TABLE app_sessions (
    token_hash   bytea       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    subject      text        NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz
);

CREATE INDEX app_sessions_subject_idx ON app_sessions (subject);
CREATE INDEX app_sessions_expires_idx ON app_sessions (expires_at);

-- +goose Down

DROP TABLE app_sessions;
DROP TABLE app_login_codes;
