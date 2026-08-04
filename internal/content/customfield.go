package content

import (
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
