package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/netip"
	"net/smtp"
	"strings"
	"syscall"
	"time"

	"github.com/emirue/ondolith/internal/auth"
)

// settingsSender delivers mail using the SMTP settings A-205 stores.
//
// It reads them per send rather than at boot: the operator configures mail from
// the running server, and a value captured at startup would mean "restart to
// send mail", which is the opposite of FR-303.
type settingsSender struct {
	settings func(keys ...string) map[string]string
	log      *slog.Logger
	// site 는 제목 앞에 붙는 사이트 이름이다. 받는 사람의 수신함에서 「이메일 인증」만으로는
	// 어느 사이트의 메일인지 모른다. nil 이면 붙이지 않는다.
	site func() string
}

// ErrMailNotConfigured is what a site without SMTP gets. It is an error rather
// than a silent success so the retry loop stops and the log says why — a mailer
// that reports success while dropping every message is worse than one that
// fails, because nobody investigates a success.
var ErrMailNotConfigured = errors.New("app: SMTP 설정이 없습니다")

func (s settingsSender) Send(ctx context.Context, to, subject, body string) error {
	kv := s.settings("mail.smtp_host", "mail.smtp_port", "mail.smtp_user",
		"mail.smtp_password", "mail.tls_mode", "mail.from_address", "mail.from_name")
	host := kv["mail.smtp_host"]
	if host == "" {
		return ErrMailNotConfigured
	}
	port := kv["mail.smtp_port"]
	if port == "" {
		port = "587"
		if kv["mail.tls_mode"] == "tls" {
			port = "465"
		}
	}
	from := kv["mail.from_address"]
	if from == "" {
		from = kv["mail.smtp_user"]
	}

	var auth smtp.Auth
	if u := kv["mail.smtp_user"]; u != "" {
		auth = smtp.PlainAuth("", u, kv["mail.smtp_password"], host)
	}
	// The body is assembled here and never logged: it carries the verification
	// and reset links, which are single-use credentials (D60).
	if s.site != nil {
		subject = mailSubject(s.site(), subject)
	}
	msg := buildMessage(kv["mail.from_name"], from, to, subject, body, time.Now())
	return sendMail(ctx, net.JoinHostPort(host, port), host, kv["mail.tls_mode"], auth, from, []string{to}, msg)
}

func mailSubject(site, subject string) string {
	if site == "" {
		return subject
	}
	return "[" + site + "] " + subject
}

// 메일 문구는 여기 한 곳이다 (FR-207, FR-214, FR-210). 경로마다 따로 쓰다 보니 사용자가
// 요청한 재설정에는 만료 안내가 있고 관리자가 강제한 재설정에는 없었다. 만료 시간은
// 토큰의 실제 수명(auth)에서 읽는다 — 숫자를 문구에 따로 적으면 둘이 갈라진다.

func verifyMailBody(link string) string {
	return fmt.Sprintf("아래 링크로 이메일 인증을 완료하세요. 링크는 %d시간 뒤 만료됩니다.\n%s",
		int(auth.EmailVerifyTTL.Hours()), link)
}

func resetMailBody(link string) string {
	return fmt.Sprintf("아래 링크로 비밀번호를 재설정하세요. 링크는 %d분 뒤 만료됩니다.\n%s",
		int(auth.PasswordResetTTL.Minutes()), link)
}

// signupAttemptBody 는 이미 있는 주소로 가입하려 할 때 그 주소의 주인에게 간다 (FR-210).
// 받는 사람이 주소의 주인이므로 「이미 가입돼 있다」고 말해도 새지 않는다.
func signupAttemptBody(base string) string {
	return "이 주소로 가입을 시도한 요청이 있었습니다. 이 주소는 이미 가입돼 있습니다.\n" +
		"본인이라면 로그인하세요: " + base + "/login\n" +
		"비밀번호가 기억나지 않으면 재설정하세요: " + base + "/password/reset\n" +
		"탈퇴했거나 이용이 중지된 계정이라면 로그인·재설정이 되지 않습니다 — 사이트 관리자에게 문의하세요.\n" +
		"본인이 아니라면 이 메일을 무시하세요."
}

// buildMessage writes the RFC 5322 message. Headers stay 7-bit: the subject and
// the display name go out as RFC 2047 encoded words, and the body as base64.
// Date and Message-ID are ours to set — SES fills them in when they are missing
// (measured 2026-10-15), but a relay that does not leaves a mail without a Date,
// which spam filters score against, and raw UTF-8 headers some clients garble.
func buildMessage(name, from, to, subject, body string, now time.Time) []byte {
	// A newline in the name would end the header and let the next line be
	// anything (Bcc). Address.String encodes control characters too; stripping
	// them first keeps that from depending on a library detail.
	name = strings.NewReplacer("\r", "", "\n", "").Replace(name)
	var b strings.Builder
	b.WriteString("From: " + (&mail.Address{Name: name, Address: from}).String() + "\r\n")
	b.WriteString("To: " + (&mail.Address{Address: to}).String() + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID(from) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 { // RFC 2045: encoded lines of at most 76 characters
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

// messageID is random and carries the sender's domain, so two mails never share
// one — a receiver may drop the second as a duplicate.
func messageID(from string) string {
	domain := "localhost"
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	return "<" + rand.Text() + "@" + domain + ">"
}

// ErrMailHostBlocked is what a host inside 169.254.0.0/16 gets — SMTP 호스트(A-205)와
// 파일 저장소 엔드포인트(A-210)가 같은 판정을 쓴다 (D60).
var ErrMailHostBlocked = errors.New("app: 링크 로컬 주소(169.254.0.0/16)에는 접속하지 않습니다")

// metadataNet is the link-local range cloud metadata services answer on
// (169.254.169.254). Only this range is refused: private and loopback relays
// are a normal way to run mail, and blocking them would break more sites than
// it protects (OPEN-43 결정, D60).
var metadataNet = netip.MustParsePrefix("169.254.0.0/16")

// blockMetadataAddr is the net.Dialer.Control hook. It runs after DNS
// resolution and immediately before connect, which is the only point that
// holds: a check on the configured host string is defeated by pointing the
// name at 169.254.169.254 after it was saved.
func blockMetadataAddr(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	if metadataNet.Contains(ap.Addr().Unmap()) {
		return fmt.Errorf("%w: %s", ErrMailHostBlocked, ap.Addr())
	}
	return nil
}

// sendMail dials through the metadata-address guard before applying the
// operator's TLS mode. smtp.SendMail owns its dial and offers no such hook.
//
// **기한이 있다.** 접속 10초, 교환 전체 30초. 없으면 방화벽이 조용히 버리는
// SMTP 호스트 하나가 시도마다 고루틴을 OS 접속 시한(리눅스 ~2분)×재시도만큼
// 붙들고, 받아 놓고 답하지 않는 서버는 영원히 붙든다 — Mailer 가 건네는 30초
// ctx 는 여기서 읽지 않으면 종이에만 있는 시한이다.
func sendMail(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	d := net.Dialer{Timeout: 10 * time.Second, Control: blockMetadataAddr}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return sendSMTP(ctx, conn, host, mode, auth, from, to, msg)
}

// sendSMTP owns the connected transport. Keeping the protocol separate from
// dialing also lets tests exercise it without opening a listening socket.
func sendSMTP(ctx context.Context, conn net.Conn, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	defer conn.Close()
	transport := conn
	stop := context.AfterFunc(ctx, func() { _ = transport.Close() })
	defer stop()
	if mode == "" {
		mode = "starttls"
	}
	if mode != "none" && mode != "starttls" && mode != "tls" {
		return errors.New("app: SMTP TLS 모드가 올바르지 않습니다")
	}
	// Even a longer caller deadline must not extend the exchange past 30s.
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		if dl.Before(deadline) {
			deadline = dl
		}
	}
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: host}
	if mode == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()

	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("app: SMTP 서버가 STARTTLS 를 지원하지 않습니다")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("app: SMTP 서버가 인증을 지원하지 않습니다")
		}
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
