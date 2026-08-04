package auth

import (
	"context"
	"log/slog"
	"time"
)

// Mail delivery is deliberately not a queue.
//
// A queue table needs an admin screen to inspect it, a retry policy nobody
// tunes, and a worker — and NFR-103 says there is no separate worker process.
// Reset and verification mails are re-requestable by the person waiting for
// them, so losing one is a dead end the user can walk out of. That is what
// makes fire-and-forget acceptable here and would not make it acceptable for,
// say, an order confirmation.

// Sender delivers one message. Implementations live outside this package;
// nothing here knows about SMTP.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Mailer retries in the background and never blocks the request.
type Mailer struct {
	send    Sender
	log     *slog.Logger
	sleep   func(time.Duration) // injectable so tests do not wait
	Retries int
	Base    time.Duration
}

func NewMailer(s Sender, log *slog.Logger) *Mailer {
	return &Mailer{send: s, log: log, sleep: time.Sleep, Retries: 3, Base: time.Second}
}

// SendAsync hands the message to a goroutine and returns immediately.
//
// The request must not fail because a mail server is slow: a visitor who
// signed up successfully would see an error and try again, creating nothing
// and understanding less. Failure is logged and the user is told to request
// the mail again if it does not arrive.
//
// The returned channel closes when delivery finishes; production ignores it,
// tests wait on it.
func (m *Mailer) SendAsync(to, subject, body string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.sendWithRetry(to, subject, body)
	}()
	return done
}

func (m *Mailer) sendWithRetry(to, subject, body string) {
	// A fresh context: the request's context is cancelled the moment the
	// response is written, which would abort every send.
	delay := m.Base
	var last error
	for attempt := 1; attempt <= m.Retries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := m.send.Send(ctx, to, subject, body)
		cancel()
		if err == nil {
			return
		}
		last = err
		if attempt < m.Retries {
			m.sleep(delay)
			delay *= 2
		}
	}
	// The address is logged; the body is not. A reset mail body contains the
	// token, and a token in the log is a token an operator can replay (C5).
	m.log.Error("메일 발송 실패", "to", to, "subject", subject,
		"attempts", m.Retries, "err", last)
}
