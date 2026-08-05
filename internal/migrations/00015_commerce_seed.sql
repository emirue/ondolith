-- +goose Up

-- Phase 3 권한과 그 전역 부여.
--
-- Phase 1·2 가 자기 권한만 심은 이유가 여기서도 그대로다: D15 4.4 의 "어떤
-- 라우트도 쓰지 않는 권한" 경고가 매 부팅 여러 건을 뱉으면 검사 자체가
-- 무시된다. 화면이 붙는 릴리즈에 그 권한을 심는다.
--
-- 전부 is_scoped = false 다. 스코프 권한은 D15 2.4 의 6개뿐이고, 그것은 게시판
-- 단위 판정을 위한 것이다 — 주문·상품에는 그런 단위가 없다.
INSERT INTO permissions (key, description, is_scoped) VALUES
    ('product.view',  '상품 관리 조회',                         false),
    ('product.manage', '상품·옵션·재고 편집',                   false),
    ('order.view',    '전체 주문 조회',                         false),
    ('order.update',  '주문 상태 전이',                         false),
    ('order.cancel',  '주문 취소',                              false),
    ('order.refund',  '환불(부분 포함)',                        false),
    ('order.return',  '반품·교환 접수·수거 확인·완료 처리',     false),
    ('payment.view',  '결제 원문·PG 응답 열람',                 false);

-- D15 2.5 의 `●` 만 넣는다. Phase 3 권한은 전부 operator 에만 붙는다 —
-- editor 는 콘텐츠 담당이고, 돈이 움직이는 화면을 여는 것은 다른 일이다.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM (VALUES
    ('operator', 'product.view'),
    ('operator', 'product.manage'),
    ('operator', 'order.view'),
    ('operator', 'order.update'),
    ('operator', 'order.cancel'),
    ('operator', 'order.refund'),
    ('operator', 'order.return'),
    ('operator', 'payment.view')
) AS g(role_key, perm_key)
JOIN roles r       ON r.key = g.role_key
JOIN permissions p ON p.key = g.perm_key;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE key IN (
    'product.view','product.manage','order.view','order.update',
    'order.cancel','order.refund','order.return','payment.view'));
DELETE FROM permissions WHERE key IN (
    'product.view','product.manage','order.view','order.update',
    'order.cancel','order.refund','order.return','payment.view');
