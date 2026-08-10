package mailer

import (
	"encoding/base64"
	"net/smtp"
	"strings"
	"testing"
)

func capture(m *Mailer) *struct {
	Addr string
	From string
	To   []string
	Msg  string
} {
	got := &struct {
		Addr string
		From string
		To   []string
		Msg  string
	}{}
	m.send = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		got.Addr, got.From, got.To, got.Msg = addr, from, to, string(msg)
		return nil
	}
	return got
}

func TestDisabledWithoutHost(t *testing.T) {
	var m Mailer
	if m.Enabled() {
		t.Fatal("zero-value Mailer must be disabled")
	}
	if err := m.Send(Message{To: Address{Email: "a@x.z"}, Subject: "s", HTML: "b"}); err != ErrDisabled {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

// A display-name From is a valid header and an invalid envelope sender —
// sending it raw earns a 501 from the SMTP server and no mail at all.
func TestSendSplitsDisplayNameFromEnvelope(t *testing.T) {
	m := New("h", "25", "", "", "Lotus <no-reply@lotus.test>")
	got := capture(m)
	if err := m.Send(Message{To: Address{Email: "a@x.z"}, Subject: "s", HTML: "b"}); err != nil {
		t.Fatal(err)
	}
	if got.From != "no-reply@lotus.test" {
		t.Errorf("envelope sender = %q, want the bare address", got.From)
	}
	if !strings.Contains(got.Msg, "From: Lotus <no-reply@lotus.test>") {
		t.Errorf("header lost the display name:\n%s", got.Msg)
	}
}

func TestSendBuildsMessage(t *testing.T) {
	m := New("smtp.example.com", "587", "u", "p", "lotus@lotus.test")
	got := capture(m)

	err := m.Send(Message{
		To:      Address{Name: "Alice", Email: "alice@example.com"},
		Subject: "You're invited to Acme",
		HTML:    "<p>hi</p>",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Addr != "smtp.example.com:587" {
		t.Errorf("addr = %q", got.Addr)
	}
	if got.From != "lotus@lotus.test" || len(got.To) != 1 || got.To[0] != "alice@example.com" {
		t.Errorf("from=%q to=%v", got.From, got.To)
	}
	if !strings.Contains(got.Msg, "To: Alice <alice@example.com>") {
		t.Errorf("missing To header with name:\n%s", got.Msg)
	}

	// The body is base64-encoded, not raw HTML, in the wire message.
	if strings.Contains(got.Msg, "<p>hi</p>") {
		t.Error("body should be base64-encoded on the wire, not raw HTML")
	}
	bodyStart := strings.Index(got.Msg, "\r\n\r\n") + 4
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(got.Msg[bodyStart:], "\r\n", ""))
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if string(decoded) != "<p>hi</p>" {
		t.Errorf("decoded body = %q, want <p>hi</p>", decoded)
	}
}

func TestSendIgnoresBlankRecipient(t *testing.T) {
	m := New("h", "25", "", "", "lotus@lotus.test")
	got := capture(m)
	if err := m.Send(Message{To: Address{Email: ""}, Subject: "s", HTML: "b"}); err != nil {
		t.Fatal(err)
	}
	if got.Addr != "" {
		t.Error("expected no send attempt for a blank recipient")
	}
}
