-- +goose Up

-- 예제 사이트의 인부 (D42 8절). 문자 로그인의 Lookup 이 이 표에서 번호를 찾는다. 번호는 site.NormalizePhone 을 거친
-- 모양(숫자만, 01…)으로 둔다 — Lookup 이 받는 모양과 같아야 찾는다.
CREATE TABLE site_example_workers (
    id         bigserial   PRIMARY KEY,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    phone      text        NOT NULL UNIQUE CHECK (phone ~ '^01[0-9]{8,9}$'),
    active     boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- 앱에서 로그인한 인부가 찍은 출퇴근은 그 인부를 가리킨다. 1단계의 앱 키만으로 찍은 기록은 비어 있다.
ALTER TABLE site_example_checkins ADD COLUMN worker_id bigint REFERENCES site_example_workers(id);

-- +goose Down

ALTER TABLE site_example_checkins DROP COLUMN worker_id;
DROP TABLE site_example_workers;
