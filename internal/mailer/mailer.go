// Package mailer sends the transactional mail Lotus produces — today, the
// organization invitation email (task 1.7). Structure mirrors talisa's own
// internal/mailer, the reference this repo uses for deploy/CI conventions.
package mailer

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Mailer is an SMTP sender. The zero value is disabled: Send reports
// ErrDisabled rather than failing halfway, so callers can treat "no SMTP
// configured" as a normal state instead of an error to log — a copyable
// invite link (task 1.6/1.7) still works with mail disabled.
type Mailer struct {
	host string
	port string
	user string
	pass string
	from string

	// send is the transport, swapped in tests. Nil means net/smtp.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// ErrDisabled means no SMTP host was configured.
var ErrDisabled = errors.New("mailer: not configured")

func New(host, port, user, pass, from string) *Mailer {
	return &Mailer{host: host, port: port, user: user, pass: pass, from: from}
}

func (m *Mailer) Enabled() bool { return m != nil && m.host != "" && m.from != "" }

// Address is one recipient. Name is optional; when set, the header carries
// "Name <email>".
type Address struct {
	Name  string
	Email string
}

// Message is one outgoing mail.
type Message struct {
	To      Address
	Subject string
	HTML    string
}

// Send delivers one HTML message.
func (m *Mailer) Send(msg Message) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	email := strings.TrimSpace(msg.To.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil
	}
	to := Address{Name: strings.TrimSpace(msg.To.Name), Email: email}

	raw := m.build(to, msg.Subject, msg.HTML)
	sendMail := m.send
	if sendMail == nil {
		sendMail = smtp.SendMail
	}
	var auth smtp.Auth
	if m.user != "" {
		auth = smtp.PlainAuth("", m.user, m.pass, m.host)
	}
	return sendMail(net.JoinHostPort(m.host, m.port), auth, envelopeFrom(m.from), []string{to.Email}, raw)
}

// envelopeFrom reduces a From setting to the bare address SMTP's MAIL FROM
// accepts. "Lotus <no-reply@...>" is a fine header value and an outright
// syntax error on the wire — servers answer it with a 501.
func envelopeFrom(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		return a.Address
	}
	return strings.Trim(from, "<> ")
}

// build assembles the RFC 5322 message. Body goes out as base64 so long
// template lines and non-ASCII names survive intact without hand-rolling
// quoted-printable soft line breaks.
func (m *Mailer) build(to Address, subject, htmlBody string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", m.from)
	fmt.Fprintf(&b, "To: %s\r\n", headerAddress(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(base64Lines(htmlBody))
	return []byte(b.String())
}

func headerAddress(a Address) string {
	if a.Name == "" {
		return a.Email
	}
	return mime.QEncoding.Encode("utf-8", a.Name) + " <" + a.Email + ">"
}

// base64Lines encodes s and wraps it at 76 characters, the limit RFC 2045
// sets for a base64 body.
func base64Lines(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}
