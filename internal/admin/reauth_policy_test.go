package admin

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// **비밀번호 재확인은 D15 5.3-1 표의 동작에서만 묻는다** (FR-726).
//
// 화면마다 묻기도 하고 안 묻기도 해 운영자가 언제 넣어야 하는지 알 수 없었다 — 표에 없는 메일 시험 발송·자동 백업
// 설정까지 물었다. 재확인을 부르는 처리기(reauthOK, 계정 조작은 guardAccountOp 를 거친다)를 코드에서 모아 표와
// 맞춘다. 처리기에 재확인을 더하거나 빼면 이 목록과 D15 5.3-1 표를 같이 고쳐야 한다 — 그것이 이 시험의 일이다.
// 한 처리기 안에서 동작마다 갈리는 것(A-604 설정 저장, A-507 거부·종료 등)은 TestRoutineSavesDoNotAskForThePassword 와
// 템플릿 쪽 TestReauthFieldOnlyWhereTheServerAsks 가 본다.
func TestReauthIsAskedExactlyWhereD15Says(t *testing.T) {
	want := []string{
		"BackupDownload",       // A-604 내려받기 (설정 저장은 뺀다)
		"BoardDelete",          // A-305
		"MailSettingsSave",     // A-205 저장 (시험 발송은 묻지 않는다)
		"PaymentSettingsSave",  // A-209 (연결 확인은 뺀다)
		"RefundSave",           // A-507 접수·실행·전체 취소
		"ReturnAction",         // A-511 환불 확정·차액 환불
		"RoleAssign",           // A-405
		"RoleGrantPermission",  // A-404
		"RoleRevoke",           // A-405
		"RoleRevokePermission", // A-404
		"SettingsSave",         // A-201 사이트 유형을 바꿀 때만
		"SocialSettingsSave",   // A-206
		"StorageSettingsSave",  // A-210 저장·연결 확인·옮기기
		"ThemeUpload",          // A-203
		"UserActivate",         // A-402 — 넷은 guardAccountOp 를 거친다
		"UserDeactivate",
		"UserDelete",
		"UserResetPassword",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	asks := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "guardAccountOp" || fn.Name.Name == "reauthOK" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if f.Name == "reauthOK" {
						asks[fn.Name.Name] = true
					}
				case *ast.SelectorExpr:
					if f.Sel.Name == "guardAccountOp" {
						asks[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	got := make([]string, 0, len(asks))
	for k := range asks {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("재확인을 묻는 처리기가 D15 5.3-1 표와 다르다\n got: %v\nwant: %v", got, want)
	}
}

// **일상 저장은 비밀번호 없이 된다** (FR-726). 운영자가 A-604 자동 백업 설정을 저장하려다 비밀번호 칸에 막혔고, 메일
// 시험 발송도 물었다. 둘은 데이터를 내보내지 않는다. 같은 화면의 내려받기와 메일 저장은 그대로 묻는다 — 위 두 개만
// 뺀 것이지 화면째 뺀 것이 아니다.
func TestRoutineSavesDoNotAskForThePassword(t *testing.T) {
	// 재확인 창이 닫혔고 비밀번호로 되살릴 수도 없는 최고 관리자다.
	su := &fakeCaller{superuser: true, reauth: true, email: "su@example.com"}
	d, _ := fixture(t, su)
	d.BackupDir = t.TempDir()
	sent := 0
	d.SendMailTest = func(context.Context, string, string, string) error { sent++; return nil }
	d.Render = func(w http.ResponseWriter, _ *http.Request, _ string, code int, _ any) { w.WriteHeader(code) }

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
		form url.Values
		want int
	}{
		{"A-604 자동 백업 설정 저장", d.BackupDownload, "/admin/backup",
			url.Values{"action": {"settings"}, "backup.auto": {"1"}, "backup.hour": {"4"}, "backup.keep": {"7"}}, http.StatusSeeOther},
		{"A-205 시험 발송", d.MailTest, "/admin/settings/mail/test", nil, http.StatusOK},
		{"A-604 지금 내려받기", d.BackupDownload, "/admin/backup", nil, http.StatusForbidden},
		{"A-604 저장된 백업 내려받기", d.BackupDownload, "/admin/backup", url.Values{"action": {"file"}, "name": {"auto-20261114-0400.zip"}}, http.StatusForbidden},
		{"A-604 모르는 동작", d.BackupDownload, "/admin/backup", url.Values{"action": {"nope"}}, http.StatusForbidden},
		{"A-205 메일 설정 저장", d.MailSettingsSave, "/admin/settings/mail", url.Values{"mail.smtp_host": {"smtp.example.com"}}, http.StatusForbidden},
	} {
		if rec := post(tc.h, tc.path, tc.form); rec.Code != tc.want {
			t.Errorf("%s = HTTP %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	if sent != 1 {
		t.Errorf("시험 메일 %d통, want 1", sent)
	}
}
