package theme

import "github.com/emirue/ondolith/internal/content"

// View is what every template receives (D17 「뷰 모델 규약」).
//
// The shape is deliberately narrow. A theme is written by a third party and
// runs with whatever it is handed, so the safest thing to hand it is the least
// that renders the page. Two rules follow from that and are asserted by tests:
//
//   - .User carries no roles and no permission rows. A theme that could read
//     them would leak the shape of the permission model into markup, and a
//     copied theme would carry that assumption to a site configured differently.
//   - Nothing here reaches configuration. The database URL holds a password
//     (C5), so it must not be reachable from any field, at any depth.
type View struct {
	Site  Site
	Menu  []*content.MenuNode
	User  *ViewUser // nil when not logged in
	Can   map[string]bool
	Flash []Flash
	Meta  Meta
	Path  string

	// Data is the screen's own payload. Each screen documents its contents in
	// D12/D13.
	Data any
}

// CanKeys is the closed set of permissions a theme may ask about.
//
// **닫힌 집합이다.** 액터가 가진 권한을 통째로 넘기면 그 순간 테마가 권한
// 모델의 모양을 읽게 되고, 복사된 테마는 다르게 설정된 사이트로 그 가정을
// 들고 간다 (위 규약). 그래서 테마가 실제로 묻는 것만 여기 적고, 늘릴 때는
// 「어느 화면이 무엇을 그릴지 정하는 데 쓰는가」를 함께 적는다.
//
//   - admin.access — 머리글의 「관리」 링크. 이것이 없던 동안 관리자는 사이트
//     어디에서도 관리 화면으로 갈 수 없었다.
var CanKeys = []string{"admin.access"}

// Site is the site-wide chrome. Only what a template draws: no DSN, no SMTP
// credentials, no secrets — those live in settings rows the handlers read
// directly and never pass on.
type Site struct {
	Name            string
	MetaDescription string
	OGImage         string
	// Type is "cms" or "shop" (FR-710). Themes branch on it to decide whether
	// to draw a cart.
	Type string
	// Business is the trader information rendered in the footer (FR-711).
	Business map[string]string
	// Turnstile is the Cloudflare Turnstile **site** key (공개값) when A-201 turned
	// the check on, else empty (FR-216). 비밀키는 여기 오지 않는다.
	Turnstile string
	// Analytics is the GA4 measurement ID (`G-…`, A-201 이 형식을 본다) or empty (FR-714).
	Analytics string
	// Maps are the map providers' browser keys for the map blocks (FR-716).
	Maps MapKeys
	// SignupClosed is A-201's 「회원가입 받지 않음」 (FR-217). 테마는 가입 링크·폼을 감춘다 —
	// 막는 것은 P-103 이고 이것은 안내다.
	SignupClosed bool
	// SEO is A-211's search-engine output (FR-718).
	SEO SEO
}

// SEO is what A-211 puts in <head>: 검색엔진 소유 확인 값(구글·네이버·빙, 공개값)과
// 「검색 노출 끄기」. 값의 모양은 A-211 이 본다(영문·숫자·-·_·=).
type SEO struct {
	GoogleVerify string
	NaverVerify  string
	BingVerify   string
	// NoIndex puts `noindex` on every public page.
	NoIndex bool
}

// MapKeys are A-201's map keys (FR-716): 네이버 ncpKeyId, 카카오 JavaScript 키, 구글 API
// 키와 Map ID. **공개값이다** — 페이지에 실려 그 지도사 SDK 를 부르고, 각 콘솔에 등록한
// 도메인에서만 동작한다. 형식은 A-201 이 본다(영문·숫자·-·_).
type MapKeys struct {
	Naver       string
	Kakao       string
	Google      string
	GoogleMapID string
}

// Any reports whether at least one provider can draw. 구글은 키와 Map ID 가 다 있어야
// 그린다 — 핀(AdvancedMarkerElement)이 Map ID 를 요구한다(Google 문서).
func (m MapKeys) Any() bool {
	return m.Naver != "" || m.Kakao != "" || (m.Google != "" && m.GoogleMapID != "")
}

// ViewUser is the logged-in caller as a template sees them: enough to greet
// them and link to their account, and nothing else.
type ViewUser struct {
	ID          string
	Email       string
	DisplayName string
}

// Flash is one message for this request.
type Flash struct {
	Kind string // "success" | "error" | "info"
	Text string
}

// Meta is the per-screen head data (FR-511).
type Meta struct {
	Title       string
	Description string
	// OGImage is an absolute URL when the site's address is known (site_url), else as stored.
	OGImage string
	// URL is the canonical address — og:url too. **비어 있으면 싣지 않는다**: site_url 이 없는
	// 설치에서 요청의 Host 로 짐작한 주소를 정본으로 알리면 잘못된 주소가 검색에 굳는다.
	URL string
	// Type is og:type — "website", or "article" for a post.
	Type string
	// JSONLD is the page's structured data (FR-719) or nil. 템플릿은
	// `<script type="application/ld+json">{{.}}</script>` 로 그대로 싣는다 — html/template 이
	// JSON 으로 이스케이프한다.
	JSONLD any
}

// NewView builds a View with every collection non-nil.
//
// D17 규약 1: nil checks are not pushed into templates. An empty list is an
// empty slice, never nil, so a theme author never has to guard — and a missing
// guard never becomes a 500 on a page that simply had no rows.
func NewView(site Site, path string) View {
	return View{
		Site:  site,
		Menu:  []*content.MenuNode{},
		Can:   map[string]bool{},
		Flash: []Flash{},
		Path:  path,
	}
}
