-- +goose Up

-- 웹훅 대조용 secret (FR-610, D50 「웹훅」).
--
-- 토스는 웹훅에 서명 헤더를 주지 않는다. 공식 문서가 제시하는 검증 수단은
-- **승인 응답이 준 `secret` 과 웹훅 본문의 `secret` 을 대조하는 것 하나뿐**이다.
-- 저장하지 않으면 대조할 상대가 없어서 그 유일한 수단이 없는 것과 같다.
--
-- 이 값만으로 진실을 삼지는 않는다. 아는 사람은 흉내낼 수 있으므로 웹훅은
-- 신호로만 쓰고 실제 상태는 조회 API 로 확인한다 (D50).
ALTER TABLE payments ADD COLUMN secret text
    CHECK (secret IS NULL OR length(secret) <= 200);

-- +goose Down

ALTER TABLE payments DROP COLUMN secret;
