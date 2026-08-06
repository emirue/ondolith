package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/smtp"
	"strings"
	"syscall"
)

// settingsSender delivers mail using the SMTP settings A-205 stores.
//
// It reads them per send rather than at boot: the operator configures mail from
// the running server, and a value captured at startup would mean "restart to
// send mail", which is the opposite of FR-303.
type settingsSender struct {
	settings func(keys ...string) map[string]string
	log      *slog.Logger
}

// ErrMailNotConfigured is what a site without SMTP gets. It is an error rather
// than a silent success so the retry loop stops and the log says why — a mailer
// that reports success while dropping every message is worse than one that
// fails, because nobody investigates a success.
var ErrMailNotConfigured = errors.New("app: SMTP 설정이 없습니다")

func (s settingsSender) Send(_ context.Context, to, subject, body string) error {
	kv := s.settings("mail.smtp_host", "mail.smtp_port", "mail.smtp_user",
		"mail.smtp_password", "mail.tls_mode", "mail.from_address", "mail.from_name")
	host := kv["mail.smtp_host"]
	if host == "" {
		return ErrMailNotConfigured
	}
	port := kv["mail.smtp_port"]
	if port == "" {
		port = "587"
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
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n%s",
		formatFrom(kv["mail.from_name"], from), to, subject, body)
	return sendMail(host+":"+port, host, auth, from, []string{to}, []byte(msg))
}

// ErrMailHostBlocked is what an SMTP host inside 169.254.0.0/16 gets.
var ErrMailHostBlocked = errors.New("app: SMTP 호스트로 링크 로컬 주소를 쓸 수 없습니다")

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

// sendMail is smtp.SendMail with the dial replaced so blockMetadataAddr can
// see the resolved address. The exchange below is the standard library's, step
// for step; smtp.SendMail owns its own dial and offers no hook.
func sendMail(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	d := net.Dialer{Control: blockMetadataAddr}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return err
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
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
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

func formatFrom(name, addr string) string {
	if name == "" {
		return addr
	}
	// A name with a quote or newline would break out of the header. Newlines
	// are header injection; the quote just ends the phrase early.
	name = strings.NewReplacer("\r", "", "\n", "", `"`, "").Replace(name)
	return `"` + name + `" <` + addr + ">"
}
