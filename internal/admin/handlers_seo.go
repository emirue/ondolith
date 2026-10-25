package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---- A-211 검색 노출 -----------------------------------------------------------

// seoSettingKeys is what A-211 owns (FR-718). 기본 설명·대표 이미지·GA4 는 A-201 에서 옮겨
// 왔다 — 사업자 정보가 A-201 로 들어오면서 사이트 설정이 길어진 몫을 여기로 덜었다.
var seoSettingKeys = []string{
	"site.meta_description", "site.og_image",
	// 방문 통계 GA4 측정 ID (FR-714). 공개 화면의 <script> 에 들어간다 — 형식은 ga4ID 가 본다.
	"analytics.ga4_id",
	// 검색엔진 소유 확인 값. 구글·네이버·빙이 홈 <head> 의 메타 태그에서 찾는다.
	"seo.google_verification", "seo.naver_verification", "seo.bing_verification",
	// 「검색 노출 끄기」 — 공개 화면 전부에 noindex. 빈 값이 「노출」이다.
	"seo.noindex",
	// robots.txt 기본 규칙 뒤에 붙는 줄.
	"seo.robots_extra",
}

// ga4ID is a GA4 measurement ID: 「G- + 숫자·문자」 (Google 애널리틱스 도움말, 2026-10-08 확인).
// 길이 상한은 문서에 없다 — 30자는 실제 ID(10자 안팎)보다 넉넉한 우리 상한이다.
var ga4ID = regexp.MustCompile(`^G-[A-Z0-9]{1,30}$`)

// ogImageURL is D19 A-211's 「스킴 허용목록 http/https 또는 내부 상대경로」. 빈 값은
// 「없음」이다. `//호스트`·`/\호스트` 는 브라우저가 다른 호스트로 읽는 경로라 내부가 아니다.
func ogImageURL(v string) bool {
	if v == "" {
		return true
	}
	if strings.HasPrefix(v, "/") {
		return !strings.HasPrefix(v, "//") && !strings.Contains(v, `\`)
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func orNone(v string) string {
	if v == "" {
		return "(없음)"
	}
	return v
}

// verifyKeyNames are the three ownership values.
var verifyKeyNames = []string{"seo.google_verification", "seo.naver_verification", "seo.bing_verification"}

// verifyToken is a console's ownership value: 구글은 base64url 모양, 네이버는 16진 40자, 빙은
// 16진 32자다(각 콘솔이 주는 태그, 2026-10-25 확인). 100자는 우리 상한이다.
var verifyToken = regexp.MustCompile(`^[A-Za-z0-9_=-]{1,100}$`)

// pastedContent finds content="…" in a pasted meta tag. 콘솔은 태그 한 줄을 주고 운영자는 그것을
// 그대로 붙여 넣는다.
var pastedContent = regexp.MustCompile(`content\s*=\s*["']([^"']*)["']`)

// robotsDirective is one line A-211 lets through: RFC 9309 의 그룹 지시어와 Sitemap, 널리 쓰는
// Crawl-delay, 그리고 주석. 그 밖의 줄은 크롤러가 무시하므로 받으면 운영자는 규칙이 적용된
// 줄 안다.
var robotsDirective = regexp.MustCompile(`(?i)^(#.*|(user-agent|allow|disallow|crawl-delay|sitemap)\s*:.*)$`)

// robotsExtraMax bounds seo.robots_extra (runes).
const robotsExtraMax = 2000

// verifyValue is what A-211 stores for a pasted ownership value — 태그면 content 값만.
func verifyValue(v string) string {
	v = strings.TrimSpace(v)
	if m := pastedContent.FindStringSubmatch(v); m != nil {
		v = strings.TrimSpace(m[1])
	}
	return v
}

// robotsRules normalises seo.robots_extra and reports the first line it refuses (1부터),
// or 0. 줄 끝은 \n 으로 맞추고 줄마다 앞뒤 공백을 걷는다.
func robotsRules(v string) (string, int) {
	// CR 하나도 줄 끝이다 (RFC 9309) — 남겨 두면 한 줄로 보이는 값 안에 다른 줄이 숨는다.
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n"), "\n")
	var out []string
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !robotsDirective.MatchString(l) {
			return "", i + 1
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), 0
}

// SEOForm is A-211 GET.
func (d *Deps) SEOForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.require(w, r, "settings.update"); !ok {
		return
	}
	d.renderSettings(w, r, "admin/seo.html", seoSettingKeys, http.StatusOK, "")
}

// SEOSave is A-211 POST. 비밀이 없는 화면이다 — 값은 전부 공개 화면이나 robots.txt 에 실린다.
func (d *Deps) SEOSave(w http.ResponseWriter, r *http.Request) {
	c, ok := d.require(w, r, "settings.update")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return
	}
	kv := map[string]string{}
	for _, k := range seoSettingKeys {
		if v := r.PostFormValue(k); v != "" || r.PostForm.Has(k) {
			kv[k] = v
		}
	}
	if utf8.RuneCountInString(kv["site.meta_description"]) > 300 {
		d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
			http.StatusUnprocessableEntity, "기본 메타 설명은 300자 이내입니다.")
		return
	}
	if !ogImageURL(kv["site.og_image"]) {
		d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
			http.StatusUnprocessableEntity, "OG 이미지는 http·https 주소이거나 /로 시작하는 이 사이트의 경로여야 합니다.")
		return
	}
	// 붙여넣은 공백·소문자는 고쳐 받는다. 빈 값은 끄기다.
	if v, ok := kv["analytics.ga4_id"]; ok {
		kv["analytics.ga4_id"] = strings.ToUpper(strings.TrimSpace(v))
		if v := kv["analytics.ga4_id"]; v != "" && !ga4ID.MatchString(v) {
			d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
				http.StatusUnprocessableEntity, "GA4 측정 ID 는 G- 로 시작하는 영문·숫자입니다 (예: G-PSW1MY7HB4).")
			return
		}
	}
	for _, k := range verifyKeyNames {
		if v, ok := kv[k]; ok {
			kv[k] = verifyValue(v)
			if kv[k] != "" && !verifyToken.MatchString(kv[k]) {
				d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
					http.StatusUnprocessableEntity, "소유 확인 값은 콘솔이 준 태그의 content 값입니다 (영문·숫자·-·_·= 100자 이내).")
				return
			}
		}
	}
	if v, ok := kv["seo.noindex"]; ok && v != "" && v != "1" {
		d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
			http.StatusUnprocessableEntity, "검색 노출은 켬 또는 끔입니다.")
		return
	}
	if v, ok := kv["seo.robots_extra"]; ok {
		if utf8.RuneCountInString(v) > robotsExtraMax {
			d.renderSettings(w, r, "admin/seo.html", seoSettingKeys,
				http.StatusUnprocessableEntity, "robots.txt 추가 규칙은 2000자 이내입니다.")
			return
		}
		rules, bad := robotsRules(v)
		if bad > 0 {
			d.renderSettings(w, r, "admin/seo.html", seoSettingKeys, http.StatusUnprocessableEntity,
				"robots.txt 추가 규칙은 User-agent·Allow·Disallow·Crawl-delay·Sitemap 줄과 # 주석만 적을 수 있습니다 ("+
					strconv.Itoa(bad)+"번째 줄).")
			return
		}
		kv["seo.robots_extra"] = rules
	}

	prev, err := d.Content.Settings(r.Context(), seoSettingKeys...)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	if err := d.Content.PutSettings(r.Context(), kv); err != nil {
		d.serverError(w, r, err)
		return
	}
	// 바뀐 키 이름만 적되 GA4 ID 는 값까지 적는다 — 방문 기록이 어느 GA 속성으로 가는지를
	// 정하는 공개값이다 (D60 「설정값을 어디에 두는가」 ①).
	var names []string
	for _, k := range seoSettingKeys {
		if !changed(kv, prev, k) {
			continue
		}
		if k == "analytics.ga4_id" {
			k += " " + orNone(prev[k]) + " → " + orNone(kv[k])
		}
		names = append(names, k)
	}
	if len(names) > 0 {
		d.log(r, c, "settings.update", "settings", "seo", "검색 노출 설정 변경: "+strings.Join(names, ", "))
	}
	http.Redirect(w, r, "/admin/settings/seo", http.StatusSeeOther)
}
