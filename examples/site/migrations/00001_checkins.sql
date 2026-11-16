-- +goose Up

-- 예제 사이트의 표 (D42). 사이트 표는 site_ 로 시작한다 — 코어를 올릴 때 코어 표와 부딪히지 않게.
CREATE TABLE site_example_checkins (
    id         bigserial   PRIMARY KEY,
    worker     text        NOT NULL CHECK (char_length(worker) BETWEEN 1 AND 40),
    kind       text        NOT NULL CHECK (kind IN ('in', 'out')),
    app        text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE site_example_checkins;
