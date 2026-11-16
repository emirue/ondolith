package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// **서명은 공식 문서의 규칙대로 따로 계산한 값과 같다** (D42 9절). 기대값은 이 코드가 아니라 openssl 로 구했다:
//
//	printf 'POST /sms/v2/services/ncp:sms:kr:123456789012:ondo-test/messages\n1700000000000\nAKEYEXAMPLE' |
//	  openssl dgst -sha256 -hmac 'SECRETEXAMPLE' -binary | openssl base64
func TestSignatureMatchesTheDocumentedRule(t *testing.T) {
	got := signature("SECRETEXAMPLE", "POST", "/sms/v2/services/ncp:sms:kr:123456789012:ondo-test/messages",
		"1700000000000", "AKEYEXAMPLE")
	if want := "4jcRNK97GRICsrAsSdJQMNYIQdcCCYFhotIfZ/fGI84="; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

// fakeSENS checks a request the way SENS does — **받은 경로로** 서명을 다시 계산한다. 서명한 경로와 실제로 나간
// 경로가 다르면(서비스 ID 의 `:` 가 인코딩되는 등) 여기서 걸린다.
func fakeSENS(t *testing.T, status int, body string) (*httptest.Server, *[]map[string]any, *int) {
	t.Helper()
	var got []map[string]any
	hits := 0 // 서버에 닿은 요청 전부 — 보내기 전에 거절해야 하는 설정이 닿았는지 본다
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		ts := r.Header.Get("x-ncp-apigw-timestamp")
		mac := hmac.New(sha256.New, []byte("SECRETEXAMPLE"))
		mac.Write([]byte(r.Method + " " + r.URL.RequestURI() + "\n" + ts + "\n" + r.Header.Get("x-ncp-iam-access-key")))
		if r.Header.Get("x-ncp-apigw-signature-v2") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			http.Error(w, `{"error":"signature"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-ncp-iam-access-key") != "AKEYEXAMPLE" || r.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
			r.URL.Path != "/sms/v2/services/ncp:sms:kr:123456789012:ondo-test/messages" {
			http.Error(w, "bad request "+r.URL.Path, http.StatusBadRequest)
			return
		}
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &hits
}

func sens(srv *httptest.Server) SENS {
	return SENS{AccessKey: "AKEYEXAMPLE", SecretKey: "SECRETEXAMPLE", ServiceID: "ncp:sms:kr:123456789012:ondo-test",
		From: "0212345678", Endpoint: srv.URL, Client: srv.Client(), Now: func() time.Time { return time.UnixMilli(1700000000000) }}
}

// **접수(202)면 성공이다** — 실제 도착은 비동기라 접수까지만 본다. 본문은 문서의 모양 그대로다.
func TestSENSSendsTheDocumentedRequest(t *testing.T) {
	srv, got, _ := fakeSENS(t, http.StatusAccepted, `{"requestId":"r1","statusCode":"202","statusName":"success"}`)
	if err := sens(srv).Send(context.Background(), "01012345678", "[사이트] 인증번호 123456 (5분)"); err != nil {
		t.Fatalf("Send = %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("받은 요청 %d개", len(*got))
	}
	m := (*got)[0]
	msgs, _ := m["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if m["type"] != "SMS" || m["from"] != "0212345678" || m["content"] != "[사이트] 인증번호 123456 (5분)" ||
		len(msgs) != 1 || first["to"] != "01012345678" {
		t.Errorf("본문 = %v", m)
	}
}

// 접수되지 않으면 오류다 — 관리자 시험 발송이 그 까닭을 화면에 보인다. 시크릿은 오류에 들지 않는다.
func TestSENSReportsWhatWentWrong(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"권한 없음":       {http.StatusUnauthorized, `{"errorMessage":"Authentication Failed"}`, "401"},
		"서버 오류":       {http.StatusInternalServerError, `oops`, "500"},
		"202 가 아닌 접수": {http.StatusAccepted, `{"statusCode":"400","statusName":"fail"}`, "400"},
		"요청 본문 오류":    {http.StatusBadRequest, `{"error":"bad"}`, "400"},
	} {
		srv, _, _ := fakeSENS(t, tc.status, tc.body)
		err := sens(srv).Send(context.Background(), "01012345678", "x")
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "SECRETEXAMPLE") {
			t.Errorf("%s: %v — %s 를 말하고 시크릿은 없어야 한다", name, err, tc.want)
		}
	}
	// 서명이 틀리면(다른 시크릿) 받는 쪽이 거절한다 — 가짜 서버가 실제로 서명을 본다는 확인이다.
	srv, _, _ := fakeSENS(t, http.StatusAccepted, `{"statusCode":"202"}`)
	s := sens(srv)
	s.SecretKey = "OTHER"
	if err := s.Send(context.Background(), "01012345678", "x"); err == nil {
		t.Error("틀린 서명이 통과했다 — 가짜 서버가 서명을 보지 않는다")
	}
	// 응답이 오지 않으면 시한에서 끊는다.
	// 본문을 다 읽어야 서버가 연결 끊김을 알아챈다 — 안 읽으면 Close 가 처리기를 영영 기다린다.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	s = sens(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Send(ctx, "01012345678", "x"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("시한 초과 = %v", err)
	}
}

// 설정이 비었거나 서비스 ID 가 경로를 바꿀 수 있는 모양이면 보내지 않는다. 호스트는 고정이지만 서비스 ID 는
// 경로에 들어간다 — `/`·`?`·`#`·`..` 로 다른 API 를 부르게 두지 않는다.
func TestSENSRefusesIncompleteOrUnsafeSettings(t *testing.T) {
	srv, _, hits := fakeSENS(t, http.StatusAccepted, `{"statusCode":"202"}`)
	for name, edit := range map[string]func(*SENS){
		"액세스 키 없음":   func(s *SENS) { s.AccessKey = "" },
		"시크릿 없음":     func(s *SENS) { s.SecretKey = "" },
		"서비스 ID 없음":  func(s *SENS) { s.ServiceID = "" },
		"발신번호 없음":    func(s *SENS) { s.From = "" },
		"서비스 ID 에 /": func(s *SENS) { s.ServiceID = "ncp:sms:kr:1:a/../../x" },
		"서비스 ID 에 ?": func(s *SENS) { s.ServiceID = "ncp:sms:kr:1:a?x=1" },
		"서비스 ID 에 #": func(s *SENS) { s.ServiceID = "ncp:sms:kr:1:a#x" },
		"서비스 ID 에 .": func(s *SENS) { s.ServiceID = "ncp:sms:kr:1:.." },
		"발신번호에 글자":   func(s *SENS) { s.From = "02-1234-5678" },
	} {
		s := sens(srv)
		edit(&s)
		if err := s.Send(context.Background(), "01012345678", "x"); err == nil {
			t.Errorf("%s: 보냈다", name)
		}
	}
	if *hits != 0 {
		t.Errorf("거절해야 할 설정으로 SENS 에 %d번 닿았다", *hits)
	}
}

// **문자는 EUC-KR 90바이트까지다** — 한글은 2바이트, ASCII 는 1바이트로 센다. 넘치면 사이트 이름을 줄인다.
// 인증번호와 시한은 줄이지 않는다.
func TestLoginTextFitsOneSMS(t *testing.T) {
	if got := LoginText("주원산림", "123456"); got != "[주원산림] 인증번호 123456 (5분)" {
		t.Errorf("짧은 이름 = %q", got)
	}
	long := LoginText(strings.Repeat("가", 60), "654321")
	if EUCKRLen(long) > 90 || !strings.HasSuffix(long, "] 인증번호 654321 (5분)") || !strings.HasPrefix(long, "[가") {
		t.Errorf("긴 이름 = %q (%d바이트)", long, EUCKRLen(long))
	}
	if got := LoginText("", "111111"); got != "인증번호 111111 (5분)" {
		t.Errorf("이름 없음 = %q", got)
	}
	for s, want := range map[string]int{"": 0, "abc": 3, "가나": 4, "a가": 3} {
		if EUCKRLen(s) != want {
			t.Errorf("EUCKRLen(%q) = %d, want %d", s, EUCKRLen(s), want)
		}
	}
}

// 전화번호는 로그에 그대로 남기지 않는다 — 가운데를 가린다.
func TestMaskHidesTheMiddle(t *testing.T) {
	for in, want := range map[string]string{"01012345678": "010****5678", "0101234567": "010***4567", "123": "***", "": ""} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
