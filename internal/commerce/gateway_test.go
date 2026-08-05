package commerce

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// D81 W3-11: 메서드는 승인·취소·조회·웹훅 검증 4개뿐이다. 추측으로 넓히지
// 않았음이 D50 과 대조된다.
//
// 개수만 세면 이름을 바꿔치기해도 통과하므로 이름까지 고정한다.
func TestGatewayHasExactlyTheFourMethodsD50Names(t *testing.T) {
	iface := reflect.TypeOf((*Gateway)(nil)).Elem()
	want := map[string]bool{"Confirm": true, "Cancel": true, "Get": true, "VerifyWebhook": true}

	if iface.NumMethod() != len(want) {
		var got []string
		for i := 0; i < iface.NumMethod(); i++ {
			got = append(got, iface.Method(i).Name)
		}
		t.Fatalf("메서드 %d개 %v, want %d개 — D50 이 넷만 필요하다고 적었다",
			iface.NumMethod(), got, len(want))
	}
	for i := 0; i < iface.NumMethod(); i++ {
		if !want[iface.Method(i).Name] {
			t.Errorf("D50 에 없는 메서드: %s", iface.Method(i).Name)
		}
	}
}

// 인터페이스에 토스페이먼츠 고유 이름이 새어나오지 않는다. 새어나오면 두 번째
// PG 는 "토스 흉내" 를 구현하게 된다.
//
// 주석은 예외다 — 사양의 출처를 적는 곳이고, 거기까지 막으면 왜 그런 필드가
// 있는지가 사라진다. 그래서 AST 의 식별자와 문자열 리터럴만 본다.
func TestNoGatewaySpecificNamesLeakIntoTheInterface(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gateway.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"toss", "tosspayments", "portone", "iamport", "kakaopay", "nicepay"}

	var found []string
	check := func(text string, pos token.Pos) {
		low := strings.ToLower(text)
		for _, b := range banned {
			if strings.Contains(low, b) {
				found = append(found, fset.Position(pos).String()+" "+text)
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			check(v.Name, v.Pos())
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				check(v.Value, v.Pos())
			}
		}
		return true
	})
	if len(found) > 0 {
		t.Errorf("인터페이스에 PG 고유 이름이 새어나왔다: %v", found)
	}
	// 검사가 헛돌지 않는지: 파일에서 식별자를 실제로 읽었는가.
	if len(file.Decls) == 0 {
		t.Fatal("gateway.go 에서 선언을 하나도 읽지 못했다")
	}
}

// FR-607. 승인 경로와 웹훅 경로가 같은 함수를 부른다 — 두 곳에 if 를 쓰면
// 한쪽만 고쳐진다.
func TestVerifyAmount(t *testing.T) {
	if err := VerifyAmount(26000, 26000); err != nil {
		t.Errorf("같은 금액 = %v", err)
	}
	for _, got := range []int{25999, 26001, 0, -26000} {
		if err := VerifyAmount(26000, got); !errors.Is(err, ErrAmountMismatch) {
			t.Errorf("저장 26000 ≠ 수신 %d = %v, want ErrAmountMismatch", got, err)
		}
	}
}

// 멱등키는 호출자가 만든다. 어댑터가 만들면 재시도마다 새 키가 나와서 멱등이
// 아니게 되는데, 재시도야말로 이 헤더가 있는 이유다 (D50 「멱등성」).
//
// 구조체에 필드가 있다는 것이 그 계약이므로, 필드가 사라지면 여기서 걸린다.
func TestIdempotencyKeyIsSuppliedByTheCaller(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"ConfirmRequest", reflect.TypeOf(ConfirmRequest{})},
		{"CancelRequest", reflect.TypeOf(CancelRequest{})},
	} {
		if _, ok := tc.typ.FieldByName("IdempotencyKey"); !ok {
			t.Errorf("%s 에 IdempotencyKey 가 없다 — 어댑터가 만들면 재시도가 멱등이 아니다", tc.name)
		}
	}
}

// D50 「10분 만료와 복구」: 승인 성공했으나 우리 DB 기록 실패가 가장 위험하고,
// 사후 대조의 유일한 근거가 응답 원문이다.
func TestPaymentCarriesTheRawResponse(t *testing.T) {
	if _, ok := reflect.TypeOf(Payment{}).FieldByName("Raw"); !ok {
		t.Error("Payment 에 Raw 가 없다 — 사후 대조의 근거가 사라진다")
	}
	// 웹훅도 마찬가지다. 본문을 진실의 근거로 삼지는 않지만, 무엇을 받았는지는
	// 남아야 A-603 이 판정할 수 있다.
	if _, ok := reflect.TypeOf(WebhookEvent{}).FieldByName("Raw"); !ok {
		t.Error("WebhookEvent 에 Raw 가 없다")
	}
	// 재전송 멱등의 키. webhook_events (pg, event_id) 유니크가 이 값을 받는다.
	if _, ok := reflect.TypeOf(WebhookEvent{}).FieldByName("EventID"); !ok {
		t.Error("WebhookEvent 에 EventID 가 없다 — 재전송을 구분할 수 없다")
	}
}

// 카드 정보는 컬럼도 필드도 없다 (DEC-3.7, PCI DSS).
func TestNoCardFieldsAnywhereInTheContract(t *testing.T) {
	banned := []string{"cardnumber", "cardno", "pan", "cvc", "cvv", "expiry", "expirydate"}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(ConfirmRequest{}), reflect.TypeOf(CancelRequest{}),
		reflect.TypeOf(Payment{}), reflect.TypeOf(WebhookEvent{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, b := range banned {
				if name == b {
					t.Errorf("%s.%s — 카드 정보는 보관하지 않는다", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}
