package theme

import (
	"reflect"
	"strings"
	"testing"
)

// D17 「뷰 모델 규약」: the seven common keys. A template that references a key
// which is not here fails to render, so the set is a contract with theme
// authors and not an implementation detail.
func TestViewHasContractKeys(t *testing.T) {
	want := []string{"Site", "Menu", "User", "Can", "Flash", "Meta", "Path"}
	ty := reflect.TypeOf(View{})
	have := map[string]bool{}
	for i := 0; i < ty.NumField(); i++ {
		have[ty.Field(i).Name] = true
	}
	for _, k := range want {
		if !have[k] {
			t.Errorf("뷰 모델에 .%s 가 없다 (D17 규약)", k)
		}
	}
}

// D17 규약 1: collections are never nil, so a theme author never writes a nil
// guard and a page with no rows never 500s for want of one.
func TestNewViewHasNoNilCollections(t *testing.T) {
	v := NewView(Site{Name: "온돌"}, "/")
	if v.Menu == nil {
		t.Error(".Menu 가 nil 이다")
	}
	if v.Can == nil {
		t.Error(".Can 이 nil 이다")
	}
	if v.Flash == nil {
		t.Error(".Flash 가 nil 이다")
	}
	// .User is the one deliberate nil: "not logged in" has to be expressible.
	if v.User != nil {
		t.Error(".User 는 미로그인 시 nil 이어야 한다")
	}
}

// The theme must not see roles or permission rows. Handing them over would put
// the permission model into third-party markup, and a theme copied to another
// site would carry assumptions that no longer hold there.
func TestViewUserCarriesNoAuthorizationData(t *testing.T) {
	ty := reflect.TypeOf(ViewUser{})
	allowed := map[string]bool{"ID": true, "Email": true, "DisplayName": true}
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if !allowed[f.Name] {
			t.Errorf(".User 에 허용되지 않은 필드: %s (역할·권한 원본은 넘기지 않는다)", f.Name)
		}
	}
	// The precomputed booleans are the only permission-shaped thing a template
	// gets, and they are display-only (D15 4.3).
	if reflect.TypeOf(View{}.Can).Kind() != reflect.Map {
		t.Error(".Can 은 미리 계산된 불리언 맵이어야 한다")
	}
}

// C5: the database URL holds a password. It must not be reachable from the view
// at any depth — not through Site, not through a nested struct somebody adds
// later. Walking the type is what makes this survive the next field.
func TestNoCredentialReachableFromView(t *testing.T) {
	banned := []string{"databaseurl", "dsn", "password", "secret", "smtppassword", "clientsecret", "token"}
	var walk func(reflect.Type, string, int)
	walk = func(ty reflect.Type, path string, depth int) {
		if depth > 6 {
			return
		}
		switch ty.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array:
			walk(ty.Elem(), path, depth+1)
			return
		case reflect.Map:
			walk(ty.Elem(), path, depth+1)
			return
		case reflect.Struct:
			for i := 0; i < ty.NumField(); i++ {
				f := ty.Field(i)
				lower := strings.ToLower(f.Name)
				for _, b := range banned {
					if lower == b {
						t.Errorf("뷰 모델에서 자격증명에 도달한다: %s.%s (C5)", path, f.Name)
					}
				}
				walk(f.Type, path+"."+f.Name, depth+1)
			}
		}
	}
	// Data is `any` by design — the screen decides. It is excluded here
	// because a type walk cannot see through an interface; screens are
	// responsible for what they put in it, which D12/D13 pin down per screen.
	walk(reflect.TypeOf(View{}), "View", 0)
}

func TestSiteCarriesOnlyRenderableFields(t *testing.T) {
	ty := reflect.TypeOf(Site{})
	allowed := map[string]bool{
		"Name": true, "MetaDescription": true, "OGImage": true,
		"Type": true, "Business": true,
	}
	for i := 0; i < ty.NumField(); i++ {
		if f := ty.Field(i); !allowed[f.Name] {
			t.Errorf("Site 에 허용되지 않은 필드: %s", f.Name)
		}
	}
}
