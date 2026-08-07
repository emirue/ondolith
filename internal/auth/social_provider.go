package auth

import (
	"fmt"
	"sort"
	"strings"

	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/google"
	"github.com/markbates/goth/providers/kakao"
	"github.com/markbates/goth/providers/naver"
)

// SocialProviders is the **code's** allow-list (D19 A-206, D15 P1).
//
// UI 에서 프로바이더를 추가할 수 없다. 데이터로 만들면 목록이 UI 데이터가 되고,
// 그때부터 「우리가 처리할 수 있는 프로바이더」와 「설정에 적힌 프로바이더」가
// 갈라진다 — 관리자는 저장에 성공했는데 로그인 버튼이 깨진다.
var SocialProviders = map[string]string{
	"google": "구글",
	"kakao":  "카카오",
	"naver":  "네이버",
}

// SocialProviderKeys is the allow-list in a stable order (화면이 쓴다).
func SocialProviderKeys() []string {
	keys := make([]string, 0, len(SocialProviders))
	for k := range SocialProviders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SocialSettingKey builds the settings key for one provider field.
//
// 키를 문자열로 조립하는 곳을 하나로 둔다. 여러 곳에서 만들면 저장하는 쪽과
// 읽는 쪽이 다른 키를 쓰는 일이 생기고, 그 증상은 "저장했는데 안 켜진다" 다.
func SocialSettingKey(provider, field string) string {
	return "social." + provider + "." + field
}

// SocialConfig is one provider's stored configuration.
type SocialConfig struct {
	Provider string
	Label    string
	ClientID string
	// HasSecret 는 값이 아니라 **있는지 여부**다. 시크릿은 저장 후 어떤
	// 화면에도 다시 오지 않는다 (D19 A-206).
	HasSecret bool
	Enabled   bool
}

// NewSocialProvider builds the goth provider for one configured entry.
//
// **`gothic` 을 쓰지 않는다.** 그 패키지는 gorilla/sessions 를 쓰는데 우리
// 세션은 scs 다 (NFR-204) — 두 세션 스토어를 한 요청에 두면 어느 쪽이 진짜
// 로그인 상태인지가 코드마다 달라진다. goth 코어의 Provider·Session 만 쓴다.
//
// callbackURL 은 **서버가 계산한다.** 입력받으면 인가 코드를 공격자 서버로
// 돌리는 경로가 된다 (D19 A-206 받지 않는 필드).
func NewSocialProvider(name, clientID, secret, callbackURL string) (goth.Provider, error) {
	if _, ok := SocialProviders[name]; !ok {
		return nil, fmt.Errorf("auth: 지원하지 않는 프로바이더입니다: %q", name)
	}
	if clientID == "" || secret == "" {
		return nil, fmt.Errorf("auth: %s 의 자격증명이 없습니다", name)
	}
	switch name {
	case "google":
		// email 만 받는다. 우리가 쓰는 것은 프로바이더 uid 와 이메일뿐이고,
		// 더 넓은 scope 는 동의 화면만 무겁게 한다.
		return google.New(clientID, secret, callbackURL, "email"), nil
	case "kakao":
		return kakao.New(clientID, secret, callbackURL, "account_email"), nil
	case "naver":
		return naver.New(clientID, secret, callbackURL), nil
	}
	return nil, fmt.Errorf("auth: 프로바이더 생성이 없습니다: %q", name)
}

// SocialCallbackURL is what the provider console must be told.
//
// 서버가 계산해 **표시만** 한다. 관리자가 이 값을 프로바이더 콘솔에 붙여넣는
// 것이 연동의 전부다.
func SocialCallbackURL(baseURL, provider string) string {
	return strings.TrimSuffix(baseURL, "/") + "/auth/" + provider + "/callback"
}
