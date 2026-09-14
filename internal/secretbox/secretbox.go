// Package secretbox seals the credentials the operator types into admin
// screens (PG 시크릿 키, SMTP 비밀번호, 소셜 client_secret) before they reach
// the settings table, and opens them for the code that uses them.
//
// **왜 있나.** 그 값들은 DB 에 평문으로 있었다. DB 덤프·백업·`pg_dump` 한 장이
// 새면 상점의 모든 승인·취소를 남이 부를 수 있었다 (`pg.secret_key`). 봉인 키는
// DB 밖 — `ondolith.json`(0600, DSN 과 같은 신뢰 경계)에 산다. 그래서 DB 만
// 새면 아무것도 풀리지 않고, 설정 파일까지 함께 새야 한다.
//
// **왜 관리자 화면이 아닌가.** 관리자가 키를 입력하면 그 키를 어딘가에 저장해야
// 하는데, 후보는 DB 뿐이고 그것이 지키려는 대상이다. 매 부팅마다 입력받는 것은
// 재시작을 사람의 손에 묶는다(NFR-103 이 배경 작업을 내장하는 이유와 같다).
// 설치 때 만들고 파일에 두는 것이 두 문제를 한 번에 없앤다.
//
// AES-256-GCM. 난수 nonce 12바이트, 태그 16바이트. **설정 키 이름이 AAD 다**:
// 봉인된 값을 다른 설정 키로 옮겨 붙여도 열리지 않는다 — `pg.secret_key` 의
// 값이 `mail.smtp_password` 자리에서 풀리면 그것은 다른 용도로 새는 길이다.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// prefix marks a sealed value. 버전이 들어가는 이유는 알고리즘을 바꿀 때 옛 값을
// 읽을 수 있어야 하기 때문이다 — 접두사가 없는 값은 봉인 전 평문이다.
const prefix = "enc:v1:"

// KeyBytes is AES-256.
const KeyBytes = 32

var (
	ErrBadKey  = errors.New("secretbox: 키는 base64 로 적힌 32바이트여야 합니다")
	ErrCorrupt = errors.New("secretbox: 봉인된 값을 열 수 없습니다 — 키가 다르거나 값이 손상됐습니다")
)

// Box seals and opens with one key.
type Box struct{ aead cipher.AEAD }

// NewKey returns a fresh random key, base64 for the config file.
func NewKey() (string, error) {
	b := make([]byte, KeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// New parses a base64 key from the config file.
func New(encoded string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != KeyBytes {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// IsSealed reports whether a stored value is a sealed one.
func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }

// Seal encrypts plaintext for the setting named name. 빈 값은 빈 값이다 —
// 「설정되지 않았다」는 비교(`!= ""`)가 봉인 뒤에도 그대로여야 한다.
func (b *Box) Seal(name, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nil, nonce, []byte(plaintext), []byte(name))
	return prefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open decrypts a stored value for the setting named name. A value without the
// prefix is returned as-is: it was written before sealing existed. The caller
// learns that through sealed=false and can re-seal it.
func (b *Box) Open(name, stored string) (plaintext string, sealed bool, err error) {
	if !IsSealed(stored) {
		return stored, false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", true, ErrCorrupt
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return "", true, fmt.Errorf("%w (%s)", ErrCorrupt, name)
	}
	return string(pt), true, nil
}
