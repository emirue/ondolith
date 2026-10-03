//go:build tosslive

// tosslive 빌드 태그 뒤에 둔다. 기본 빌드에 들어가면 `make test-integration`
// 이 네트워크와 토스 계정을 요구하게 되고, 키가 없는 환경에서는 SKIP 이
// 생긴다 — 그 SKIP 을 이 저장소의 게이트가 거부한다 (scripts/check-testrun.sh).
//
// 실행:
//	ONDOLITH_TOSS_TEST_SECRET=test_sk_... make test-toss

package commerce

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func liveToss(t *testing.T) *Toss {
	t.Helper()
	secret := os.Getenv("ONDOLITH_TOSS_TEST_SECRET")
	if secret == "" {
		// 건너뛰지 않고 실패한다. 이 파일은 태그를 붙여야만 빌드되므로,
		// 여기까지 왔다는 것은 실행하겠다고 말한 것이다.
		t.Fatal("ONDOLITH_TOSS_TEST_SECRET 이 없다 — A-209 에 넣은 테스트 시크릿 키를 환경변수로 준다")
	}
	if !strings.HasPrefix(secret, "test_") {
		// **값을 찍지 않는다.** 접두사가 아니라는 사실만으로 충분하고,
		// 짧은 값에 `secret[:5]` 를 하면 패닉한다 — 거부해야 할 자리에서
		// 패닉하면 거부 메시지가 나오지 않는다.
		t.Fatal("ONDOLITH_TOSS_TEST_SECRET 이 test_ 로 시작하지 않는다 — 라이브 키로 이 검사를 돌리지 않는다")
	}
	return NewToss(secret, "https://api.tosspayments.com", AuthWindow)
}

// **자격증명이 실제로 받아들여진다.**
//
// 없는 `paymentKey` 를 조회하면 토스는 구조화된 오류(NOT_FOUND 계열)를 준다.
// 우리 키가 틀렸다면 그 전에 401 이 오고, 그것은 다른 오류로 매핑된다 —
// 즉 **이 검사가 통과한다는 것은 Basic 인증 인코딩이 맞다는 뜻**이다
// (`test_sk_x:` 의 콜론까지 포함해서).
func TestLiveTossAcceptsOurCredentials(t *testing.T) {
	tp := liveToss(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := tp.Get(ctx, "no-such-payment-key-"+time.Now().UTC().Format("20060102150405"))
	if err == nil {
		t.Fatal("없는 결제를 조회했는데 성공했다")
	}
	// 전송·파싱 실패가 아니라 **API 가 판단한 거부**여야 한다.
	if errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("전송·파싱 단계에서 끝났다 (%v) — 어댑터가 API 와 말이 통하지 않는다", err)
	}
	if strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("인증이 거부됐다 (%v) — 시크릿 키나 Basic 인코딩이 틀렸다", err)
	}
	if !strings.Contains(err.Error(), "HTTP 4") {
		t.Fatalf("4xx 가 아니다: %v", err)
	}
	t.Logf("조회 거부를 정상적으로 받았다: %v", err)
}

// **금액 조작 요청이 승인되지 않는다** (W3-34 ②).
//
// 없는 결제 키로 승인을 시도한다. 토스가 거부해야 하고, 그 거부가 우리 쪽
// 오류로 매핑되어야 한다 — 여기서 `ErrPaymentUnknown` 이 나오면 우리는 결과를
// 모르는 상태로 남고, D50 은 그때 조회로 확인하라고 적었다.
func TestLiveTossRefusesUnknownPayment(t *testing.T) {
	tp := liveToss(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := tp.Confirm(ctx, ConfirmRequest{
		OrderNo:        "LIVE-" + time.Now().UTC().Format("20060102150405"),
		PaymentKey:     "no-such-payment-key",
		Amount:         1000,
		IdempotencyKey: "live-probe-" + time.Now().UTC().Format("20060102150405"),
	})
	if err == nil {
		t.Fatal("없는 결제 키로 승인이 성공했다")
	}
	// **전송·파싱 실패는 통과가 아니다.** 네트워크가 막힌 환경에서 이 검사가
	// 초록이면, 「승인 경로도 같은 자격증명으로 통한다」를 아무것도 확인하지
	// 않고 보고하게 된다 — 이 파일이 존재하는 이유가 그 확인 하나다.
	if errors.Is(err, ErrPaymentUnknown) {
		t.Fatalf("전송·파싱 단계에서 끝났다 (%v) — API 에 닿지 못했다", err)
	}
	if strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("인증이 거부됐다 (%v) — 시크릿 키가 틀렸다", err)
	}
	if !strings.Contains(err.Error(), "HTTP 4") {
		t.Fatalf("4xx 가 아니다: %v", err)
	}
	t.Logf("승인 거부를 정상적으로 받았다: %v", err)
}

// **시크릿이 오류 문자열로 새지 않는다.** 오류는 로그와 화면으로 흘러간다.
func TestLiveTossErrorsCarryNoSecret(t *testing.T) {
	secret := os.Getenv("ONDOLITH_TOSS_TEST_SECRET")
	tp := liveToss(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := tp.Get(ctx, "no-such-payment-key")
	if err == nil {
		t.Fatal("없는 결제를 조회했는데 성공했다")
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("오류 문자열에 시크릿 키가 들어 있다")
	}
	// 어댑터 값을 통째로 찍어도 새지 않는다 (String()/GoString() 가 가린다).
	for _, s := range []string{err.Error(), tp.String(), tp.GoString()} {
		if strings.Contains(s, secret) {
			t.Errorf("시크릿이 노출된다: %.60s", s)
		}
	}
}

// **가상계좌는 발급만으로는 결제완료가 아니다 — 실제 조회 API 로 확인한다.**
//
// P-905 는 입금 알림을 받으면 저장된 paymentKey 로 Get 을 부르고, 그 답이
// 승인(DONE)일 때만 결제완료로 옮긴다. 단위 테스트는 가짜 게이트웨이를 쓰므로
// 「토스가 발급 직후의 가상계좌를 조회에 실제로 어떻게 답하는가」는 여기서만
// 확인된다: 대기로 읽혀야 하고(승인으로 읽으면 입금 없이 물건이 나간다), 웹훅
// 대조 상대인 secret 이 실려 와야 하며 발급 응답의 것과 같아야 한다.
//
// 발급은 API 개별 연동 키가 필요하다 — 결제위젯 문서용 키는 이 경로에서
// NOT_FOUND_MERCHANT 다 (2026-10-02 실측). 입금(DONE)은 개발자센터
// 「테스트 결제내역」의 입금처리로만 만들 수 있어 자동 검사가 닿지 않는다.
func TestLiveTossVirtualAccountReadsAsPendingWithItsSecret(t *testing.T) {
	tp := liveToss(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	orderNo := "LIVEVA-" + time.Now().UTC().Format("20060102150405")
	issued, err := tp.post(ctx, "/v1/virtual-accounts", map[string]any{
		"amount": 1000, "orderId": orderNo, "orderName": "가상계좌 실측",
		"customerName": "실측", "bank": "88", "validHours": 1,
	}, "live-va-"+orderNo)
	if err != nil {
		t.Fatalf("가상계좌 발급 = %v (API 개별 연동 테스트 키인지 확인)", err)
	}
	if issued.PaymentKey == "" || issued.Secret == "" {
		t.Fatalf("발급 응답에 paymentKey·secret 이 없다: key=%t secret=%t",
			issued.PaymentKey != "", issued.Secret != "")
	}

	got, err := tp.Get(ctx, issued.PaymentKey)
	if err != nil {
		t.Fatalf("발급한 가상계좌 조회 = %v", err)
	}
	if got.Status != PaymentPending {
		t.Errorf("입금 전 가상계좌가 %s 로 읽혔다, want %s", got.Status, PaymentPending)
	}
	if got.OrderNo != orderNo || got.Amount != 1000 {
		t.Errorf("조회 = (%q, %d), want (%q, 1000)", got.OrderNo, got.Amount, orderNo)
	}
	// 입금 알림의 secret 은 이것과 같아야 정상 요청이다 (토스 웹훅 문서).
	if got.Secret != issued.Secret {
		t.Error("조회 응답의 secret 이 발급 응답의 것과 다르다 — 웹훅 대조 상대가 흔들린다")
	}
	t.Logf("발급 %s → 조회 %s (secret 일치)", orderNo, got.Status)
}
