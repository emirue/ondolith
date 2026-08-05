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
// 서버가 만든다. 폼에서 받으면 같은 키를 두 번 보내 접수를 막거나, 매번 다른
// 키를 보내 이중 접수를 만들 수 있다 — 멱등 키의 목적이 뒤집힌다.
func NewRequestKey() (string, error) { return newRandomKey() }
