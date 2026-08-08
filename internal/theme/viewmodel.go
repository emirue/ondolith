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
	OGImage     string
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
