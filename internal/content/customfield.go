package content

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FieldType is the closed vocabulary D30 gives board_fields.field_type.
type FieldType string

const (
	FieldText        FieldType = "text"
	FieldTextarea    FieldType = "textarea"
	FieldNumber      FieldType = "number"
	FieldSelect      FieldType = "select"
	FieldCheckbox    FieldType = "checkbox"
	FieldMultiselect FieldType = "multiselect"
	FieldDate        FieldType = "date"
	FieldURL         FieldType = "url"
)

// FieldSchema is one row of board_fields as the validator needs it.
type FieldSchema struct {
	Key        string
	Label      string
	Type       FieldType
	Required   bool
	ShowInList bool
	Options    []string
	Sort       int
}

var (
	ErrUnknownField  = errors.New("content: 스키마에 없는 필드")
	ErrFieldRequired = errors.New("content: 필수 필드")
	ErrFieldType     = errors.New("content: 값의 형식이 필드 타입과 맞지 않습니다")
	ErrFieldOption   = errors.New("content: 선택지에 없는 값")
)

// FieldError names the field so the form can put the message next to the input
// rather than at the top of the page.
type FieldError struct {
	Key string
	Err error
}

func (e FieldError) Error() string { return e.Key + ": " + e.Err.Error() }
func (e FieldError) Unwrap() error { return e.Err }

// reservedFieldKeys are the post's own columns. A custom field with one of
// these names would shadow the real column everywhere the two are merged for a
// template, and D30 deliberately does not block them in the database — the list
// would grow with every column added, and a CHECK constraint cannot say which
// name collided or why.
var reservedFieldKeys = map[string]bool{
	"id": true, "board_id": true, "author_id": true, "title": true, "body": true,
	"status": true, "is_pinned": true, "is_secret": true, "view_count": true,
	"created_at": true, "updated_at": true, "custom_fields": true,
}

// ValidateFieldKey is A-306's check on a new field name.
func ValidateFieldKey(key string) error {
	if reservedFieldKeys[key] {
		return fmt.Errorf("%w: %q 는 글의 기본 항목 이름입니다", ErrFieldType, key)
	}
	return nil
}

// ValidateCustomFields turns submitted form values into the JSONB object a post
// stores, using the board's schema as the only authority.
//
// D14 3절 규칙 2: only keys the schema defines are accepted, and an undefined
// key is REFUSED rather than dropped. Dropping is the dangerous half of mass
// assignment — the request looks like it worked, so nobody learns that the form
// and the schema disagree, and the same silence would hide a genuine attempt to
// write a key nobody defined.
//
// prev carries the values already stored. Fields the administrator has since
// deleted are not in the schema, so they cannot be submitted — but their stored
// values are carried through unchanged (규칙 4): deleting a field stops it being
// shown, it does not destroy what people wrote.
func ValidateCustomFields(schema []FieldSchema, form url.Values, prev map[string]any) (map[string]any, error) {
	known := make(map[string]FieldSchema, len(schema))
	for _, f := range schema {
		known[f.Key] = f
	}

	// Refuse before writing anything, so a rejected post leaves no partial row.
	for key := range form {
		if _, ok := known[key]; !ok {
			return nil, FieldError{Key: key, Err: ErrUnknownField}
		}
	}

	out := make(map[string]any, len(schema)+len(prev))
	// Values whose field was deleted survive; a field still in the schema is
	// re-derived from the form below, so it is not copied here.
	for k, v := range prev {
		if _, stillDefined := known[k]; !stillDefined {
			out[k] = v
		}
	}

	for _, f := range schema {
		v, err := coerceField(f, form)
		if err != nil {
			return nil, FieldError{Key: f.Key, Err: err}
		}
		if v == nil {
			// An optional field left blank is absent, not null: an absent key
			// keeps the JSONB smaller and reads the same in a template.
			continue
		}
		out[f.Key] = v
	}
	return out, nil
}

func coerceField(f FieldSchema, form url.Values) (any, error) {
	switch f.Type {
	case FieldCheckbox:
		// A checkbox posts nothing when off. There is no "missing" state, so
		// required has nothing to add — it is always one of the two values.
		return form.Has(f.Key) && form.Get(f.Key) != "", nil

	case FieldMultiselect:
		vals := form[f.Key]
		var picked []any
		for _, v := range vals {
			if v == "" {
				continue
			}
			if !contains(f.Options, v) {
				return nil, fmt.Errorf("%w: %q", ErrFieldOption, v)
			}
			picked = append(picked, v)
		}
		if len(picked) == 0 {
			if f.Required {
				return nil, ErrFieldRequired
			}
			return nil, nil
		}
		return picked, nil
	}

	raw := strings.TrimSpace(form.Get(f.Key))
	if raw == "" {
		if f.Required {
			return nil, ErrFieldRequired
		}
		return nil, nil
	}

	switch f.Type {
	case FieldText, FieldTextarea:
		return raw, nil

	case FieldNumber:
		// Stored as a JSON number so that sorting and comparison in the
		// database mean what they look like. "10" < "9" as text.
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: 숫자가 아닙니다 (%q)", ErrFieldType, raw)
		}
		return n, nil

	case FieldSelect:
		if !contains(f.Options, raw) {
			return nil, fmt.Errorf("%w: %q", ErrFieldOption, raw)
		}
		return raw, nil

	case FieldDate:
		if _, err := time.Parse("2006-01-02", raw); err != nil {
			return nil, fmt.Errorf("%w: 날짜는 YYYY-MM-DD 입니다 (%q)", ErrFieldType, raw)
		}
		return raw, nil

	case FieldURL:
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%w: 주소가 아닙니다 (%q)", ErrFieldType, raw)
		}
		// http(s) only. A `javascript:` value rendered into an href is stored
		// XSS, and the template cannot tell it apart from a real link.
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("%w: http 또는 https 만 허용합니다 (%q)", ErrFieldType, u.Scheme)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%w: 알 수 없는 필드 타입 %q", ErrFieldType, f.Type)
}

func contains(list []string, v string) bool {
	for _, o := range list {
		if o == v {
			return true
		}
	}
	return false
}

// FieldInput pairs a field definition with the value a form should show.
//
// The pairing is built in Go because html/template has no way to make one:
// there is no `dict`, and D17 closes the function map (adding one function for
// this would be the first of several). It also keeps the type branching in a
// single partial, which is what W2-19 asks for.
type FieldInput struct {
	Field FieldSchema
	Value any
}

// FieldInputs zips a board's schema with a post's stored values, in schema
// order. A value whose field was deleted is not returned — it is preserved in
// the database (D14 3절 규칙 4) but there is no input to draw for it.
func FieldInputs(schema []FieldSchema, values map[string]any) []FieldInput {
	out := make([]FieldInput, 0, len(schema))
	for _, f := range schema {
		out = append(out, FieldInput{Field: f, Value: values[f.Key]})
	}
	return out
}

// FieldTypes is the closed vocabulary A-306's dropdown offers, in one place so
// that the screen and the validator cannot list different sets.
func FieldTypes() []FieldType {
	return []FieldType{
		FieldText, FieldTextarea, FieldNumber, FieldSelect,
		FieldCheckbox, FieldMultiselect, FieldDate, FieldURL,
	}
}

// ---- 회원 프로필 항목 (FR-215) ----------------------------------------------
//
// **게시판 커스텀 필드와 같은 기계를 쓴다** (DEC-3.9). 스키마·검증·폼 생성이
// 이미 여기 있으므로, 회원 항목을 위해 그것을 한 벌 더 만들면 두 벌이 되고
// 갈라진 쪽은 조용히 낡는다. 다른 것은 「어디에 정의가 있고 값이 어디 있는가」
// 뿐이다: 게시판은 board_fields·posts.custom_fields, 회원은
// user_fields·users.custom_fields.

// UserFields lists the profile fields an operator defined, in display order.
func (s *Store) UserFields(ctx context.Context) ([]FieldSchema, error) {
	const q = `
		SELECT key, label, field_type, is_required, show_in_list, options, sort_order
		FROM user_fields ORDER BY sort_order, key`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldSchema
	for rows.Next() {
		var f FieldSchema
		var opts []string
		if err := rows.Scan(&f.Key, &f.Label, &f.Type, &f.Required,
			&f.ShowInList, &opts, &f.Sort); err != nil {
			return nil, err
		}
		f.Options = opts
		out = append(out, f)
	}
	return out, rows.Err()
}

// SaveUserField inserts or updates one definition.
//
// 예약어 검사는 여기서도 한다 — 저장 직전이 마지막 방어선이고, 화면이 하나
// 늘 때마다 그 화면이 검사를 빠뜨릴 수 있다 (SaveBoardField 와 같은 이유).
func (s *Store) SaveUserField(ctx context.Context, f FieldSchema) error {
	if err := ValidateFieldKey(f.Key); err != nil {
		return err
	}
	opts := f.Options
	if opts == nil {
		opts = []string{}
	}
	const q = `
		INSERT INTO user_fields (key, label, field_type, is_required, show_in_list, options, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (key) DO UPDATE SET
			label = EXCLUDED.label, field_type = EXCLUDED.field_type,
			is_required = EXCLUDED.is_required, show_in_list = EXCLUDED.show_in_list,
			options = EXCLUDED.options, sort_order = EXCLUDED.sort_order, updated_at = now()`
	_, err := s.pool.Exec(ctx, q, f.Key, f.Label, f.Type,
		f.Required, f.ShowInList, opts, f.Sort)
	return err
}

// DeleteUserField removes a definition. **회원이 적어 낸 값은 남는다**
// (D14 3절 규칙 4) — 항목을 잘못 지운 운영자가 사람들의 입력까지 잃지 않도록.
func (s *Store) DeleteUserField(ctx context.Context, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM user_fields WHERE key = $1`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
