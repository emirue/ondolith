package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBlockMetadataAddr(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"169.254.169.254:25", true},          // the cloud metadata endpoint
		{"169.254.0.0:587", true},             // range start
		{"169.254.255.255:587", true},         // range end
		{"[::ffff:169.254.169.254]:25", true}, // same address, IPv4-mapped
		{"169.253.255.255:25", false},         // one below the range
		{"169.255.0.0:25", false},             // one above the range
		{"127.0.0.1:25", false},               // a local relay is normal
		{"10.0.0.1:25", false},                // a private relay is normal
		{"[fe80::1]:25", false},               // 권고는 169.254.0.0/16 만이다
		{"8.8.8.8:587", false},
	}
	for _, tc := range cases {
		err := blockMetadataAddr("tcp", tc.addr, nil)
		if got := errors.Is(err, ErrMailHostBlocked); got != tc.blocked {
			t.Errorf("blockMetadataAddr(%q): blocked=%v, want %v (err=%v)",
				tc.addr, got, tc.blocked, err)
		}
	}
}

// TestSendMailRefusesMetadataHost proves the Control hook is actually wired
// into the dialer sendMail uses. Nothing listens on that address; Control runs
// before connect, so the call returns our error rather than a timeout.
func TestSendMailRefusesMetadataHost(t *testing.T) {
	err := sendMail(context.Background(), "169.254.169.254:25", "169.254.169.254", "none", nil,
		"a@example.com", []string{"b@example.com"}, []byte("hi"))
	if !errors.Is(err, ErrMailHostBlocked) {
		t.Fatalf("sendMail to the metadata address: %v, want ErrMailHostBlocked", err)
	}
}

// TestSendMailDeliversToServer exercises the whole exchange against a real
// socket. sendMail reimplements smtp.SendMail's flow to get a dial hook, so a
// dropped step (EHLO, DATA terminator, QUIT) has to fail here.
func TestSendMailDeliversToServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- "accept: " + err.Error()
			return
		}
		defer conn.Close()
		got <- fakeSMTP(conn)
	}()

	msg := []byte("Subject: 확인\r\n\r\n본문입니다")
	err = sendMail(context.Background(), ln.Addr().String(), "127.0.0.1", "none", nil,
		"from@example.com", []string{"to@example.com"}, msg)
	if err != nil {
		t.Fatalf("sendMail: %v", err)
	}

	transcript := <-got
	for _, want := range []string{
		"EHLO localhost",
		"MAIL FROM:<from@example.com>",
		"RCPT TO:<to@example.com>",
		"DATA",
		"본문입니다",
		"QUIT",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("서버가 %q 를 받지 못했다\n전체:\n%s", want, transcript)
		}
	}
}

// fakeSMTP speaks just enough SMTP to accept one message and returns what the
// client sent. It advertises no extension, so STARTTLS and AUTH stay out of
// the path this test is about.
func fakeSMTP(conn net.Conn) string {
	return fakeSMTPWithSTARTTLS(conn, false)
}

func fakeSMTPWithSTARTTLS(conn net.Conn, startTLS bool) string {
	var seen strings.Builder
	r := bufio.NewReader(conn)
	write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

	write("220 fake ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return seen.String()
		}
		line = strings.TrimRight(line, "\r\n")
		seen.WriteString(line + "\n")

		if inData {
			if line == "." {
				inData = false
				write("250 2.0.0 queued")
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			if startTLS {
				write("250-fake\r\n250 STARTTLS")
			} else {
				write("250 fake")
			}
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			write("250 2.0.0 OK")
		case line == "DATA":
			inData = true
			write("354 go ahead")
		case line == "QUIT":
			write("221 2.0.0 bye")
			return seen.String()
		default:
			write("500 5.5.1 unknown")
		}
	}
}

// 메일 원문은 RFC 5322·2047 을 따른다 (FR-708). SES 는 빠진 Date·Message-ID 를
// 붙이고 한글 제목을 인코딩해 줬지만(2026-10-15 Gmail 수신 원문으로 실측), 그러지
// 않는 릴레이로 나가면 Date 없는 메일은 스팸 점수를 받고 8비트 머리글은 깨질 수
// 있다 — 고쳐 주는 쪽에 기대지 않는다.
func TestBuildMessageIsWellFormed(t *testing.T) {
	now := time.Date(2026, 10, 15, 13, 25, 41, 0, time.UTC)
	body := "아래 링크로 비밀번호를 재설정하세요.\nhttps://example.com/password/reset/abc"
	raw := buildMessage("온돌샵", "noreply@ondolith.com", "buyer@example.com", "비밀번호 재설정", body, now)

	head := raw[:bytes.Index(raw, []byte("\r\n\r\n"))]
	for _, c := range head {
		if c >= 0x80 {
			t.Fatalf("머리글에 8비트 문자가 있다 (RFC 2047 인코딩 없음):\n%s", head)
		}
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("메일로 읽히지 않는다: %v\n%s", err, raw)
	}
	dec := new(mime.WordDecoder)
	if subj, _ := dec.DecodeHeader(m.Header.Get("Subject")); subj != "비밀번호 재설정" {
		t.Errorf("제목 = %q", subj)
	}
	from, err := m.Header.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Name != "온돌샵" || from[0].Address != "noreply@ondolith.com" {
		t.Errorf("From = %v (%v)", from, err)
	}
	if d, err := m.Header.Date(); err != nil || !d.Equal(now) {
		t.Errorf("Date = %v (%v), want %v", d, err, now)
	}
	if id := m.Header.Get("Message-ID"); !regexp.MustCompile(`^<[^@\s<>]+@ondolith\.com>$`).MatchString(id) {
		t.Errorf("Message-ID = %q, want <…@ondolith.com>", id)
	}
	if cte := m.Header.Get("Content-Transfer-Encoding"); cte != "base64" {
		t.Errorf("Content-Transfer-Encoding = %q, want base64", cte)
	}
	encoded, _ := io.ReadAll(m.Body)
	lines := strings.Split(strings.TrimRight(string(encoded), "\r\n"), "\r\n")
	if len(lines) < 2 {
		t.Fatalf("본문이 한 줄뿐이다 — 이 단언은 줄을 나눠야 하는 길이를 전제한다: %q", encoded)
	}
	for _, l := range lines {
		if len(l) > 76 {
			t.Errorf("base64 줄이 %d자다 — RFC 2045 는 76자까지: %q", len(l), l)
		}
	}
	b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(encoded)))
	if string(b) != body {
		t.Errorf("본문 = %q, want %q", b, body)
	}
	if bytes.Equal(raw, buildMessage("온돌샵", "noreply@ondolith.com", "buyer@example.com", "비밀번호 재설정", body, now)) {
		t.Error("Message-ID 가 매번 같다 — 수신 측이 같은 메일로 보고 버린다")
	}
}

// 보내는 이름은 관리자가 적는 값이다. 줄바꿈이 머리글을 끝내면 Bcc 를 끼워 넣어
// 우리 SMTP 로 아무에게나 보낼 수 있다.
func TestBuildMessageNameCannotInjectHeaders(t *testing.T) {
	raw := buildMessage("온돌\r\nBcc: evil@example.com", "noreply@ondolith.com", "buyer@example.com", "제목", "본문", time.Now())
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if bcc := m.Header.Get("Bcc"); bcc != "" {
		t.Errorf("이름의 줄바꿈으로 Bcc 머리글이 생겼다: %q", bcc)
	}
}

// SMTP 가 비어 있어서 실패한 것은 작업 로그에 남기지 않는다. 그 상태는 대시보드가
// 알리고, 남기면 공개 재설정(IP당 3회/시간)·가입 요청마다 지울 수 없는 행이 하나씩
// 쌓여 외부에서 작업 로그를 채울 수 있다. 설정이 있는데 실패한 것은 남긴다.
func TestMailFailureEntrySkipsUnconfiguredSMTP(t *testing.T) {
	if _, ok := mailFailureEntry("a@example.com", "비밀번호 재설정", ErrMailNotConfigured); ok {
		t.Error("SMTP 미설정 실패를 작업 로그에 남긴다 — 외부 요청으로 로그가 쌓인다")
	}
	e, ok := mailFailureEntry("a@example.com", "비밀번호 재설정", errors.New("dial tcp 10.0.0.1:587: connect: connection refused"))
	if !ok {
		t.Fatal("설정이 있는데 실패한 발송을 남기지 않는다")
	}
	if e.ActorEmail != "system" || e.Action != "mail.failed" || e.TargetID != "비밀번호 재설정" ||
		!strings.Contains(e.Summary, "a@example.com") || !strings.Contains(e.Summary, "connection refused") {
		t.Errorf("작업 로그 항목 = %+v", e)
	}
}

// **같은 메일은 어느 경로로 나가든 같은 문구다** (FR-207, FR-214, FR-210).
//
// 사용자가 요청한 재설정은 「30분 뒤 만료됩니다」, 관리자가 강제한 재설정은 「아래 링크로
// 재설정하세요:」뿐이었다. 인증 메일에는 24시간 만료 안내가 없었고, 제목에 사이트 이름이
// 없었다. 「가입 시도 알림」은 「본인이 아니라면 무시하세요」뿐이라 본인이면 할 일이 없었다.
func TestMailCopyCarriesExpiryAndSiteName(t *testing.T) {
	link := "https://example.com/password/reset/abc"
	if b := resetMailBody(link); !strings.Contains(b, "30분") || !strings.Contains(b, link) {
		t.Errorf("재설정 메일에 만료 시간이나 링크가 없다: %q", b)
	}
	vlink := "https://example.com/verify/abc"
	if b := verifyMailBody(vlink); !strings.Contains(b, "24시간") || !strings.Contains(b, vlink) {
		t.Errorf("인증 메일에 만료 시간이나 링크가 없다: %q", b)
	}
	b := signupAttemptBody("https://example.com")
	for _, want := range []string{"https://example.com/login", "https://example.com/password/reset", "무시"} {
		if !strings.Contains(b, want) {
			t.Errorf("가입 시도 알림에 %q 가 없다: %q", want, b)
		}
	}
	if got := mailSubject("온돌샵", "이메일 인증"); got != "[온돌샵] 이메일 인증" {
		t.Errorf("제목 = %q — 어느 사이트의 메일인지 모른다", got)
	}
	if got := mailSubject("", "이메일 인증"); got != "이메일 인증" {
		t.Errorf("사이트 이름이 없을 때 제목 = %q", got)
	}
}
