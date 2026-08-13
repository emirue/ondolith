package commerce

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Reconcile 은 Store 의 메서드지만 DB 를 하나도 쓰지 않는다 — 조회는 전부 gw 가
// 한다. 그래서 DSN 없이 도는 자리이고, 여기 있는 단언은 `make check` 에서 실제로
// 실행된다.

// **PG 를 「사용 안 함」으로 바꾸면 여기로 nil 이 들어온다.** pgAdapterFor 는
// 등록되지 않은 제공자에 ("", nil) 을 돌려주고, 예전 payments 행이 조회 구간에
// 남아 있으면 A-508 이 그대로 이 함수를 부른다. 앞선 판은 `gw.Get` 에서 nil
// 인터페이스 역참조로 패닉했다 — 관리자 화면이 응답 도중에 죽었고, 이 저장소에
// recover 는 없다.
func TestReconcileWithoutGatewayMarksRowsInsteadOfPanicking(t *testing.T) {
	rows := []ReconcileRow{
		{PaymentKey: "k1", OurStatus: "대기", OurAmount: 1000},
		{PaymentKey: "k2", OurStatus: "승인", OurAmount: 2000},
	}

	out := (&Store{}).Reconcile(context.Background(), nil, rows)

	if len(out) != len(rows) {
		t.Fatalf("행 %d 개가 들어가 %d 개가 나왔다 — 행을 잃으면 안 보이는 결제가 생긴다", len(rows), len(out))
	}
	for i, r := range out {
		// **조회하지 않은 것을 「일치」로 그리지 않는다.** Diff 가 비면 화면은
		// 그 행을 정상으로 표시하고, 돈이 나갔는데 주문에 반영되지 않은 행이
		// 초록으로 보인다 — 대사가 존재하는 이유가 뒤집힌다.
		if r.Diff == "" {
			t.Errorf("행 %d: PG 를 묻지 않았는데 Diff 가 비었다 (일치로 보인다)", i)
		}
		if !strings.Contains(r.Diff, "설정") {
			t.Errorf("행 %d: Diff = %q — 왜 조회하지 않았는지 읽히지 않는다", i, r.Diff)
		}
		if r.TheirStatus != "" || r.TheirAmount != 0 {
			t.Errorf("행 %d: 묻지도 않고 PG 쪽 값을 채웠다 (%q, %d)", i, r.TheirStatus, r.TheirAmount)
		}
	}
}

func TestReconcileWithoutGatewayOnEmptyRows(t *testing.T) {
	if out := (&Store{}).Reconcile(context.Background(), nil, nil); len(out) != 0 {
		t.Fatalf("빈 입력에 %d 행이 나왔다", len(out))
	}
}

// 가드가 넓게 잡혀 대사 자체를 삼키면 조용히 아무것도 확인하지 않는 화면이 된다.
// 정상 경로가 여전히 PG 를 묻는지 같이 잠근다.
func TestReconcileStillAsksTheGatewayWhenItExists(t *testing.T) {
	gw := &fakeGateway{getResponse: &Payment{Status: PaymentApproved, Amount: 1000}}
	out := (&Store{}).Reconcile(context.Background(), gw,
		[]ReconcileRow{{PaymentKey: "k1", OurStatus: "대기", OurAmount: 1000}})

	if gw.getCalls != 1 {
		t.Fatalf("PG 조회가 %d 회다 — 1 회여야 한다", gw.getCalls)
	}
	if len(out) != 1 || out[0].Diff == "" {
		t.Fatalf("PG 는 승인, 우리는 대기인데 차이가 잡히지 않았다: %+v", out)
	}
	// D50 이 「가장 위험한 상태」라고 부르는 자리다 — 돈은 나갔는데 주문은 대기다.
	if !strings.Contains(out[0].Diff, "돈이 나갔는데") {
		t.Errorf("가장 위험한 상태의 문구가 아니다: %q", out[0].Diff)
	}
}

func TestReconcileReportsGatewayError(t *testing.T) {
	gw := &fakeGateway{getErr: errors.New("타임아웃")}
	out := (&Store{}).Reconcile(context.Background(), gw,
		[]ReconcileRow{{PaymentKey: "k1", OurStatus: "승인", OurAmount: 1000}})

	if len(out) != 1 || !strings.Contains(out[0].Diff, "조회 실패") {
		t.Fatalf("조회 실패가 행에 남지 않았다: %+v", out)
	}
}
