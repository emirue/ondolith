package commerce

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// orderNoAlphabet excludes the characters people confuse when reading a number
// off a screen and typing it into P-504.
//
// Dropped: 0/O, 1/I/L (L reads as lowercase l in many fonts), 2/Z, 5/S, 8/B.
// A support call that starts with "it says my order number is wrong" is a real
// cost, and it is paid by whoever chose a denser alphabet.
const orderNoAlphabet = "34679ACDEFGHJKMNPQRTUVWXY"

// orderNoRandomLen is how many random characters follow the date.
//
// 25^10 ≈ 9.5e13. With a UNIQUE index catching the rest, this is far past the
// point where a collision matters — the length is chosen so the number stays
// readable over the phone, not to reach a probability target.
const orderNoRandomLen = 10

// NewOrderNo builds an order number.
//
// It is NOT a sequence. A sequential number lets anyone holding one order
// number walk the neighbouring orders, and P-504 takes exactly that number as
// input for guest lookup (SC-3 3항). The date prefix is for humans reading a
// list; the entropy is in the random tail.
func NewOrderNo(now time.Time) string {
	b := make([]byte, orderNoRandomLen)
	rand.Read(b) // #nosec G104 -- crypto/rand.Read panics rather than failing
	out := make([]byte, 0, 8+orderNoRandomLen+1)
	out = now.UTC().AppendFormat(out, "20060102")
	out = append(out, '-')
	for _, v := range b {
		out = append(out, orderNoAlphabet[int(v)%len(orderNoAlphabet)])
	}
	return string(out)
}

// newRandomKey is 32 bytes of crypto/rand, URL-safe. 장바구니 키처럼 "추측되면
// 곧 접근" 인 값에 쓴다.
func newRandomKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewRequestKey is the idempotency key for a refund request.
//
// 서버가 만든다. 구매자 화면(P-507)은 POST 마다 만든다 — 폼에서 받으면 같은
// 키로 남의 접수를 막거나 키를 바꿔 이중 접수를 만들 수 있다. 관리자 화면
// (A-507)은 **화면을 그릴 때 만들어 폼이 되돌려 준다** (D19 A-507): 그래야 같은
// 폼의 재전송(더블클릭·502 새로고침)이 같은 키로 와서 UNIQUE 가 알아본다.
// 그쪽은 주문 범위를 붙여 저장하므로(AdminRefundKey) 다른 주문의 키와 겹치지 않는다.
func NewRequestKey() (string, error) { return newRandomKey() }

// ValidRequestKey reports whether s has NewRequestKey's shape. 폼이 되돌려 준 값을
// 쓰기 전에 본다 — 모양이 다르면 이 서버가 발급한 것이 아니다.
func ValidRequestKey(s string) bool {
	if len(s) != 32 { // base64url(24바이트), 패딩 없음
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// AdminRefundKey scopes a form-returned key to one order, so a key can only ever
// collide with a resubmission of the same order's form.
func AdminRefundKey(orderID, formKey string) string { return "A-507:" + orderID + ":" + formKey }

// MemberRefundKey is P-507's key: P-508 이 발급한 폼 키를 그 주문에 묶는다 — AdminRefundKey 와 같은
// 이유이고, 접두사가 달라 두 화면의 키는 겹치지 않는다.
func MemberRefundKey(orderID, formKey string) string { return "P-507:" + orderID + ":" + formKey }

// NewReturnNo is the same shape as an order number, with an R prefix.
//
// 순번이 아닌 이유도 같다 (SC-3 3항): A-511 목록과 P-513 이 이 번호를 쓰고,
// 순번이면 하나로 남의 반품 건을 훑을 수 있다.
func NewReturnNo(now time.Time) string { return "R" + NewOrderNo(now) }
