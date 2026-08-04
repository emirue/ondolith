package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
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
	return smtp.SendMail(host+":"+port, auth, from, []string{to}, []byte(msg))
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
