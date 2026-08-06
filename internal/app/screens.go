package app

// screenInventory is D11's screen table as data: id → security class.
//
// It is a copy, and copies drift (.ai/MISTAKES.md M9). scripts/checkdocs.sh
// compares it to docs/11-screens.md on every build, which is the same
// arrangement the RBAC seed has with D15 — the doc stays the source, the code
// gets a value it can act on at boot, and disagreement breaks the build rather
// than a running site.
//
// The boot check needs it to answer "is this screen one D11 declares, and under
// the class D11 gives it" (D15 4.4 검사 5). Screens with no route yet are still
// listed: their absence from the route table is a phase boundary, not an error.
var screenInventory = map[string]SecurityClass{
	"P-001": SC2, // 설치 마법사
	"P-002": SC1, // 설치 유도 리다이렉트
	"P-101": SC2, // 로그인
	"P-102": SC2, // 로그아웃
	"P-103": SC2, // 회원가입
	"P-104": SC2, // 비밀번호 재설정 요청
	"P-105": SC2, // 비밀번호 재설정 수행
	"P-106": SC2, // 소셜 로그인 시작
	"P-107": SC2, // 소셜 로그인 콜백
	"P-108": SC3, // 내 정보
	"P-109": SC3, // 비밀번호 변경
	"P-110": SC3, // 회원 탈퇴
	"P-111": SC3, // 소셜 계정 연결 관리
	"P-112": SC2, // 이메일 인증 확인
	"P-113": SC2, // 인증 메일 재발송
	"P-201": SC1, // 홈
	"P-202": SC1, // 정적 페이지
	"P-203": SC1, // 게시판 목록
	"P-204": SC1, // 글 보기
	"P-205": SC2, // 글 쓰기
	"P-206": SC3, // 글 수정
	"P-207": SC3, // 글 삭제
	"P-208": SC2, // 댓글 작성
	"P-209": SC3, // 댓글 수정
	"P-210": SC3, // 댓글 삭제
	"P-211": SC7, // 첨부파일 다운로드
	"P-212": SC1, // 검색 결과
	"P-301": SC1, // 상품 목록
	"P-302": SC1, // 카테고리별 목록
	"P-303": SC1, // 상품 상세
	"P-304": SC1, // 옵션 조합 조회 (htmx)
	"P-305": SC1, // 상품 검색
	"P-401": SC2, // 장바구니 담기
	"P-402": SC1, // 장바구니 보기
	"P-403": SC3, // 장바구니 수량 변경
	"P-404": SC3, // 장바구니 항목 삭제
	"P-405": SC6, // 주문서 작성
	"P-406": SC6, // 주문 생성
	"P-407": SC6, // 결제창 호출
	"P-408": SC6, // 결제 승인 (successUrl)
	"P-409": SC6, // 결제 실패 (failUrl)
	"P-410": SC3, // 주문 완료
	"P-501": SC3, // 내 주문 목록
	"P-502": SC3, // 주문 상세
	"P-503": SC2, // 비회원 주문 조회 폼
	"P-504": SC2, // 비회원 주문 조회 실행
	"P-505": SC3, // 배송 조회
	"P-506": SC6, // 취소 요청
	"P-507": SC6, // 부분 환불 요청
	"P-508": SC3, // 취소·환불 상태
	"P-509": SC3, // 주문서·영수증
	"P-510": SC3, // 구매확정
	"P-511": SC6, // 반품 요청
	"P-512": SC6, // 교환 요청
	"P-513": SC3, // 반품·교환 내역
	"P-514": SC6, // 교환 차액 결제
	"P-901": SC1, // sitemap.xml
	"P-902": SC1, // robots.txt
	"P-903": SC1, // 404 오류
	"P-904": SC1, // 500 오류
	"P-905": SC8, // 결제 웹훅 수신
	"P-906": SC7, // 테마 정적 자산
	"P-907": SC1, // 헬스체크
	"A-101": SC4, // 대시보드
	"A-102": SC4, // 관리자 셸 (레이아웃·메뉴)
	"A-201": SC5, // 사이트 설정
	"A-202": SC5, // 테마 목록·활성화
	"A-203": SC7, // 테마 업로드
	"A-204": SC5, // 메뉴 관리
	"A-205": SC5, // 메일 발송 설정
	"A-206": SC5, // 소셜 로그인 설정
	"A-207": SC5, // 약관 관리
	"A-208": SC5, // 사업자 정보 설정
	"A-209": SC5, // 결제 설정
	"A-301": SC4, // 페이지 목록
	"A-302": SC5, // 페이지 편집
	"A-303": SC5, // 페이지 발행
	"A-304": SC4, // 게시판 목록
	"A-305": SC5, // 게시판 생성·설정
	"A-306": SC5, // 커스텀 필드 스키마 편집기
	"A-307": SC5, // 글 관리
	"A-308": SC5, // 댓글 관리
	"A-309": SC7, // 첨부파일 관리
	"A-401": SC4, // 사용자 목록
	"A-402": SC5, // 사용자 상세·편집
	"A-403": SC5, // 역할 목록·정의
	"A-404": SC5, // 역할 권한 편집
	"A-405": SC5, // 사용자 역할 부여
	"A-501": SC4, // 상품 목록
	"A-502": SC7, // 상품 편집
	"A-503": SC5, // 옵션·재고 편집기
	"A-504": SC4, // 주문 목록
	"A-505": SC4, // 주문 상세
	"A-506": SC5, // 주문 상태 변경
	"A-507": SC6, // 취소·환불 처리
	"A-508": SC6, // 결제 대사
	"A-509": SC5, // 상품 카테고리 관리
	"A-510": SC5, // 배송 정보·송장 입력
	"A-511": SC6, // 반품·교환 처리
	"A-512": SC5, // 커머스 정책 설정
	"A-513": SC4, // QR 라벨 발행
	"A-514": SC5, // 스캔 입고
	"A-515": SC5, // 재고 실사
	"A-516": SC5, // 출고 피킹 대조
	"A-517": SC4, // 스캔 재고 조회
	"A-601": SC4, // 작업 로그
	"A-602": SC4, // 시스템 정보
	"A-603": SC4, // 웹훅 수신 이력
}

// servedOutsideTree lists screens that are deliberately not in the route table.
//
// **목록이지 예외가 아니다** — 여기 적힌 화면은 "구현하지 않아도 된다" 가
// 아니라 "다른 문으로 받는다" 는 뜻이고, 그 문에는 그것대로 테스트가 있다.
// 이름을 여기 적는 것만으로 미구현이 숨겨지지 않도록, 각 항목은 어디서 서비스
// 되는지를 적는다.
var servedOutsideTree = map[string]bool{
	// P-905 결제 웹훅: webhookMux 가 본 트리 밖에서 받는다 (D15 SC-8 1항).
	// 세션·CSRF·액터가 붙지 않아야 하고, `CrossOriginProtection` 이 통과시키는
	// 것은 브라우저 헤더가 없어서 생기는 우연이라 그것에 기대지 않는다.
	"P-905": true,
}
