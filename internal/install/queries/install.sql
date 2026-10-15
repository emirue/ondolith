-- name: CreateFirstAdmin :execrows
WITH created AS (
    INSERT INTO users (email, password_hash, display_name)
    VALUES ($1, $2, $3)
    ON CONFLICT (email) DO NOTHING
    RETURNING id
)
INSERT INTO user_roles (user_id, role_id)
SELECT created.id, r.id FROM created, roles r WHERE r.key = 'admin';

-- name: SeedDefaultSettings :exec
-- FR-214: 이메일 인증은 기본 켬이다. 설정 행이 없으면 꺼짐으로 읽히므로 설치가 넣는다.
-- **회원이 하나도 없는 DB 에만** 넣는다. 설정 파일을 잃고 같은 DB 로 마법사를 다시
-- 돌리면(다른 관리자 이메일이면 진행된다) 이 기본값 이전에 설치해 행이 없는 운영
-- 사이트의 인증이 켜진다. 있는 값은 건드리지 않는다.
INSERT INTO settings (key, value)
SELECT 'auth.email_verification_required', '1'
WHERE NOT EXISTS (SELECT 1 FROM users)
ON CONFLICT (key) DO NOTHING;
