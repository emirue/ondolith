package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emirue/ondolith/internal/content"
)

// Theme-declared admin screens (D41). A theme names the screens and their
// fields in admin.json; the core draws them, stores the values and hands them
// to templates as .Theme. Everything here is pure — the store and the handlers
// live elsewhere.

const (
	KindSettings   = "settings"
	KindCollection = "collection"

	FieldText     = "text"
	FieldTextarea = "textarea"
	FieldNumber   = "number"
	FieldImage    = "image"
	FieldURL      = "url"
	FieldSelect   = "select"

	// D41 2절 상한.
	MaxAdminBytes  = 64 << 10
	MaxScreens     = 20
	MaxFields      = 30
	MaxImageFields = 10 // 편집기 한 번 업로드와 같은 수 (admin.editorMaxFiles)
	maxOptions     = 50

	maxText     = 500
	maxTextarea = 5000
	maxURL      = 2000
	maxNumber   = 1e15 // 이 아래의 정수는 float64 로 정확하고 템플릿에서 지수 없이 찍힌다

	// ClearSuffix 는 사진 칸의 「사진 빼기」 체크 이름에 붙는다. 칸 키에 쓸 수 없는
	// 글자(:)라 선언된 칸 이름과 겹치지 않는다.
	ClearSuffix = ":clear"

	defaultGroup = "테마"
)

// adminKey is the screen and field key rule — 게시판 커스텀 필드 키와 같다. 템플릿이
// `.Theme.hero.headline` 으로 바로 쓸 수 있는 이름이어야 한다.
var adminKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// ValidKey reports whether s can be a screen key — 라우트의 {key} 를 거르는 데도 쓴다.
func ValidKey(s string) bool { return adminKey.MatchString(s) }

// Screen is one declared admin screen.
type Screen struct {
	Key    string
	Title  string
	Group  string
	Order  int
	Kind   string
	Fields []Field
}

// Collection reports whether the screen holds rows (모음형) rather than one set of values.
func (s Screen) Collection() bool { return s.Kind == KindCollection }

// ImageFields counts the picture fields — 업로드 본문 상한이 이 수에 비례한다 (D41 6절).
func (s Screen) ImageFields() int {
	n := 0
	for _, f := range s.Fields {
		if f.Type == FieldImage {
			n++
		}
	}
	return n
}

// Field is one declared input. Default is already in the form templates receive
// (string, int64 or float64), or nil.
type Field struct {
	Key      string
	Label    string
	Type     string
	Required bool
	Default  any
	Help     string
	Options  []string
}

// Image is what a template gets for a picture field. 빈 값이면 URL 이 비어 있다.
type Image struct {
	URL    string
	Thumb  string
	Width  int
	Height int
}

// FieldError names the field a refusal belongs to, so the form can put the
// message beside the input.
type FieldError struct {
	Key     string
	Label   string
	Message string
}

func (e FieldError) Error() string { return e.Label + ": " + e.Message }

// ReadAdmin reads admin.json from a theme directory. A missing file is no
// screens and no warnings — 선언은 선택이다. OpenInRoot 가 심볼릭 링크로 테마 밖을
// 읽는 것을 막는다 (로고 검사와 같은 방식).
func ReadAdmin(dir string) ([]Screen, []string) {
	if dir == "" {
		return nil, nil
	}
	f, err := os.OpenInRoot(dir, "admin.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{"admin.json 을 열 수 없다 — 테마 화면이 없다"}
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxAdminBytes+1))
	if err != nil {
		return nil, []string{"admin.json 을 읽을 수 없다 — 테마 화면이 없다"}
	}
	return ParseAdmin(b)
}

// ParseAdmin turns admin.json into screens. **잘못된 화면만 뺀다** (D41 지배 원칙 5):
// a file-level fault drops every screen with one warning; a fault inside one
// screen drops that screen with a warning that names it.
func ParseAdmin(b []byte) ([]Screen, []string) {
	if len(b) > MaxAdminBytes {
		return nil, []string{fmt.Sprintf("admin.json 이 %d KiB 를 넘는다 — 테마 화면이 없다", MaxAdminBytes>>10)}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, []string{"admin.json 이 JSON 객체가 아니다 — 테마 화면이 없다"}
	}
	rawScreens, ok := top["screens"]
	if !ok {
		return nil, []string{"admin.json 에 screens 가 없다 — 테마 화면이 없다"}
	}
	var list []json.RawMessage
	if err := json.Unmarshal(rawScreens, &list); err != nil {
		return nil, []string{"admin.json 의 screens 가 배열이 아니다 — 테마 화면이 없다"}
	}

	var (
		out    []Screen
		warns  []string
		seen   = map[string]bool{}
		capped bool
	)
	for i, raw := range list {
		s, err := parseScreen(raw)
		if err != nil {
			name := s.Key
			if name == "" {
				name = fmt.Sprintf("%d번째", i+1)
			}
			warns = append(warns, fmt.Sprintf("admin.json: 화면 %s — %v (이 화면은 빠진다)", name, err))
			continue
		}
		if seen[s.Key] {
			warns = append(warns, fmt.Sprintf("admin.json: 화면 키 %s 가 겹친다 — 뒤의 것은 빠진다", s.Key))
			continue
		}
		if len(out) == MaxScreens {
			if !capped {
				warns = append(warns, fmt.Sprintf("admin.json: 화면이 %d개를 넘는다 — %d번째부터 빠진다", MaxScreens, MaxScreens+1))
				capped = true
			}
			continue
		}
		seen[s.Key] = true
		out = append(out, s)
	}
	return out, warns
}

type rawScreen struct {
	Key    string            `json:"key"`
	Title  string            `json:"title"`
	Group  *string           `json:"group"`
	Order  int               `json:"order"`
	Kind   string            `json:"kind"`
	Fields []json.RawMessage `json:"fields"`
}

type rawField struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Default  json.RawMessage `json:"default"`
	Help     string          `json:"help"`
	Options  []string        `json:"options"`
}

// strict decodes with unknown properties refused — 오타(`lable`)가 조용히 무시되면
// 테마 작성자는 왜 라벨이 안 나오는지 모른다.
func strict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func parseScreen(raw json.RawMessage) (Screen, error) {
	var r rawScreen
	if err := strict(raw, &r); err != nil {
		return Screen{}, fmt.Errorf("읽을 수 없다: %v", err)
	}
	s := Screen{Key: r.Key, Title: r.Title, Group: defaultGroup, Order: r.Order, Kind: r.Kind}
	switch {
	case !adminKey.MatchString(r.Key):
		return s, errors.New("key 는 영소문자로 시작하는 영소문자·숫자·밑줄 32자까지다")
	case !runeLen(r.Title, 1, 50):
		return s, errors.New("title 은 1~50자다")
	case r.Kind != KindSettings && r.Kind != KindCollection:
		return s, errors.New("kind 는 settings 또는 collection 이다")
	case len(r.Fields) < 1 || len(r.Fields) > MaxFields:
		return s, fmt.Errorf("fields 는 1~%d개다", MaxFields)
	}
	if r.Group != nil {
		if !runeLen(*r.Group, 1, 30) {
			return s, errors.New("group 은 1~30자다")
		}
		s.Group = *r.Group
	}
	keys := map[string]bool{}
	for _, fr := range r.Fields {
		f, err := parseField(fr)
		if err != nil {
			return s, err
		}
		if keys[f.Key] {
			return s, fmt.Errorf("칸 키 %s 가 겹친다", f.Key)
		}
		keys[f.Key] = true
		s.Fields = append(s.Fields, f)
	}
	if n := s.ImageFields(); n > MaxImageFields {
		return s, fmt.Errorf("사진 칸은 %d개까지다 (%d개)", MaxImageFields, n)
	}
	return s, nil
}

func parseField(raw json.RawMessage) (Field, error) {
	var r rawField
	if err := strict(raw, &r); err != nil {
		return Field{}, fmt.Errorf("칸을 읽을 수 없다: %v", err)
	}
	f := Field{Key: r.Key, Label: r.Label, Type: r.Type, Required: r.Required, Help: r.Help, Options: r.Options}
	switch {
	case !adminKey.MatchString(r.Key):
		return f, fmt.Errorf("칸 key %q 는 영소문자로 시작하는 영소문자·숫자·밑줄 32자까지다", r.Key)
	case !runeLen(r.Label, 1, 100):
		return f, fmt.Errorf("칸 %s 의 label 은 1~100자다", r.Key)
	case !slices.Contains([]string{FieldText, FieldTextarea, FieldNumber, FieldImage, FieldURL, FieldSelect}, r.Type):
		return f, fmt.Errorf("칸 %s 의 type %q 는 text·textarea·number·image·url·select 중 하나가 아니다", r.Key, r.Type)
	case utf8.RuneCountInString(r.Help) > 200:
		return f, fmt.Errorf("칸 %s 의 help 는 200자까지다", r.Key)
	}
	if err := checkOptions(f); err != nil {
		return f, err
	}
	if len(r.Default) > 0 && string(r.Default) != "null" {
		d, err := parseDefault(f, r.Default)
		if err != nil {
			return f, fmt.Errorf("칸 %s 의 default — %v", r.Key, err)
		}
		f.Default = d
	}
	return f, nil
}

func checkOptions(f Field) error {
	if f.Type != FieldSelect {
		if f.Options != nil {
			return fmt.Errorf("칸 %s 는 select 가 아니라 options 를 가질 수 없다", f.Key)
		}
		return nil
	}
	if len(f.Options) < 1 || len(f.Options) > maxOptions {
		return fmt.Errorf("칸 %s 의 options 는 1~%d개다", f.Key, maxOptions)
	}
	seen := map[string]bool{}
	for _, o := range f.Options {
		if !runeLen(o, 1, 100) || seen[o] {
			return fmt.Errorf("칸 %s 의 options 는 1~100자이고 겹치지 않는다", f.Key)
		}
		seen[o] = true
	}
	return nil
}

// parseDefault checks a default against the field's own save rule and returns it
// in the shape templates get.
func parseDefault(f Field, raw json.RawMessage) (any, error) {
	if f.Type == FieldImage {
		return nil, errors.New("사진 칸은 기본값을 가질 수 없다")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var text string
	switch x := v.(type) {
	case string:
		if f.Type == FieldNumber {
			return nil, errors.New("숫자 칸의 기본값은 JSON 숫자다")
		}
		text = x
	case float64:
		if f.Type != FieldNumber {
			return nil, errors.New("이 칸의 기본값은 문자열이다")
		}
		text = strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return nil, errors.New("기본값은 문자열이나 숫자다")
	}
	c, err := f.Coerce(text)
	if err != nil {
		return nil, err
	}
	return viewValue(f, c), nil
}

func runeLen(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

// Coerce validates one submitted value for every type but image (D41 2절 표).
// Blank is nil — 값이 없는 것이다.
func (f Field) Coerce(raw string) (any, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	switch f.Type {
	case FieldText:
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("한 줄로 적습니다.")
		}
		if utf8.RuneCountInString(v) > maxText {
			return nil, fmt.Errorf("%d자까지입니다.", maxText)
		}
		return v, nil
	case FieldTextarea:
		if utf8.RuneCountInString(v) > maxTextarea {
			return nil, errors.New("5,000자까지입니다.")
		}
		return v, nil
	case FieldNumber:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("숫자가 아닙니다.")
		}
		if math.Abs(n) >= maxNumber {
			return nil, errors.New("절댓값이 1,000조보다 작아야 합니다.")
		}
		return n, nil
	case FieldURL:
		if utf8.RuneCountInString(v) > maxURL {
			return nil, errors.New("2,000자까지입니다.")
		}
		if !linkOK(v) {
			return nil, errors.New("http·https 주소나 / 로 시작하는 사이트 안 경로만 받습니다.")
		}
		return v, nil
	case FieldSelect:
		if !slices.Contains(f.Options, v) {
			return nil, errors.New("고를 수 있는 값이 아닙니다.")
		}
		return v, nil
	}
	return nil, fmt.Errorf("이 칸(%s)은 글로 받지 않습니다.", f.Type)
}

// Apply merges one submitted form into the stored values of a screen (or of one
// row). **선언된 칸만 바꾼다** — prev 의 다른 키는 그대로 남는다 (D41 지배 원칙 2).
//
// uploads are the picture fields that came with a file. A nil value means the
// file is not stored yet: the handler calls Apply once with nils to check the
// form before writing any file, then again with the stored pictures. 그래야
// 거부된 폼이 사진 파일을 남기지 않는다.
func (s Screen) Apply(prev map[string]any, form url.Values, uploads map[string]map[string]any) (map[string]any, []FieldError) {
	out := make(map[string]any, len(prev)+len(s.Fields))
	for k, v := range prev {
		out[k] = v
	}
	var errs []FieldError
	for _, f := range s.Fields {
		present := false
		if f.Type == FieldImage {
			img, uploading := uploads[f.Key]
			switch {
			case uploading:
				if img != nil {
					out[f.Key] = img
				}
				present = true
			case form.Get(f.Key+ClearSuffix) != "":
				delete(out, f.Key)
			}
			// 다른 테마가 같은 키에 남긴 사진 아닌 값은 채운 것이 아니다 — 템플릿은 빈 사진을 받는다.
			present = present || viewValue(f, out[f.Key]) != nil
		} else {
			v, err := f.Coerce(form.Get(f.Key))
			if err != nil {
				errs = append(errs, FieldError{Key: f.Key, Label: f.Label, Message: err.Error()})
				continue
			}
			if v == nil {
				delete(out, f.Key)
			} else {
				out[f.Key] = v
			}
			present = v != nil
		}
		if f.Required && f.Default == nil && !present {
			errs = append(errs, FieldError{Key: f.Key, Label: f.Label, Message: "필수입니다."})
		}
	}
	return out, errs
}

// ScreenValues is one screen's (or one row's) values as templates read them:
// the stored value when it fits the declaration, else the default, else the
// type's empty value. **선언된 칸은 전부 키가 있다** (D17 규약 1).
//
// A stored value that does not fit — another theme declared the same key with a
// different type, or an option was removed — is treated as absent rather than
// breaking the page. 링크는 읽을 때도 다시 본다: 다른 테마의 글 칸이 남긴
// `javascript:` 가 링크로 나가면 안 된다.
func ScreenValues(s Screen, stored map[string]any) map[string]any {
	out := make(map[string]any, len(s.Fields))
	for _, f := range s.Fields {
		v := viewValue(f, stored[f.Key])
		if v == nil {
			v = f.Default
		}
		if v == nil {
			v = zeroValue(f)
		}
		out[f.Key] = v
	}
	return out
}

func viewValue(f Field, v any) any {
	switch f.Type {
	case FieldText, FieldTextarea:
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	case FieldURL:
		if s, ok := v.(string); ok && s != "" && linkOK(s) {
			return s
		}
	case FieldSelect:
		if s, ok := v.(string); ok && slices.Contains(f.Options, s) {
			return s
		}
	case FieldNumber:
		if n, ok := v.(float64); ok && !math.IsNaN(n) && math.Abs(n) < maxNumber {
			// 실수 1000000 은 템플릿에서 1e+06 으로 찍힌다 — 소수부가 없으면 정수로 넘긴다.
			if n == math.Trunc(n) {
				return int64(n)
			}
			return n
		}
	case FieldImage:
		m, _ := v.(map[string]any)
		id, _ := m["id"].(string)
		if !content.IsUUID(id) {
			return nil
		}
		w, _ := m["w"].(float64)
		h, _ := m["h"].(float64)
		return Image{URL: "/media/" + id, Thumb: "/media/" + id + "/thumb", Width: int(w), Height: int(h)}
	}
	return nil
}

// Value is a stored value as templates read it, or nil when it is absent or does not
// fit the declaration — 기본값은 대신하지 않는다. 폼이 미리 채울 값이다: 맞지 않는 옛 값을
// 채우면 운영자가 건드리지 않은 칸 때문에 저장이 거부된다.
func (f Field) Value(v any) any { return viewValue(f, v) }

// linkOK is A-211's link rule plus no control characters. 브라우저는 URL 의 탭·줄바꿈을 지우므로
// `/\t/evil.example` 은 JS 문맥에서 `//evil.example` 이 된다 — href 는 %09 로 막아도 테마가 어디에 쓸지 모른다.
func linkOK(s string) bool {
	return content.IsLinkURL(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func zeroValue(f Field) any {
	switch f.Type {
	case FieldNumber:
		return nil
	case FieldImage:
		return Image{}
	}
	return ""
}

// BuildThemeData is .Theme: settings screens as one value map, collections as
// their rows in order (빈 목록이지 nil 이 아니다). nil when nothing is declared.
func BuildThemeData(screens []Screen, settings map[string]map[string]any, items map[string][]map[string]any) map[string]any {
	if len(screens) == 0 {
		return nil
	}
	out := make(map[string]any, len(screens))
	for _, s := range screens {
		if !s.Collection() {
			out[s.Key] = ScreenValues(s, settings[s.Key])
			continue
		}
		rows := make([]map[string]any, 0, len(items[s.Key]))
		for _, data := range items[s.Key] {
			rows = append(rows, ScreenValues(s, data))
		}
		out[s.Key] = rows
	}
	return out
}

// AdminScreens is the active theme's declared screens (D41). 개발 모드가 아니면
// 테마를 바꿀 때마다 로더가 새로 만들어지므로 한 번 읽는다 — Brand 와 같다.
// 경고는 활성화(ValidateThemeDir)와 부팅 로그가 낸다.
func (l *Loader) AdminScreens() []Screen {
	switch {
	case l.dir == "":
		return nil
	case l.dev:
		s, _ := ReadAdmin(l.dir)
		return s
	}
	l.adminOnce.Do(func() { l.admin, _ = ReadAdmin(l.dir) })
	return l.admin
}
