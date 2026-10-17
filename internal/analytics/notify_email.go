package analytics

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// Email goes through the SMTP server in VISIONARY_SMTP_HOST, _PORT (587),
// _USER, _PASSWORD and _FROM. The connection is upgraded with STARTTLS (or
// TLS from the start on port 465); a server that offers neither is refused
// unless it is on this machine, so credentials never cross a network in the
// clear.

type smtpSettings struct {
	Host, User, Password, From string
	Port                       int
}

func smtpFromEnv() (smtpSettings, bool) {
	c := smtpSettings{
		Host:     strings.TrimSpace(os.Getenv("VISIONARY_SMTP_HOST")),
		User:     os.Getenv("VISIONARY_SMTP_USER"),
		Password: os.Getenv("VISIONARY_SMTP_PASSWORD"),
		From:     strings.TrimSpace(os.Getenv("VISIONARY_SMTP_FROM")),
		Port:     587,
	}
	if p, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VISIONARY_SMTP_PORT"))); err == nil && p > 0 && p < 65536 {
		c.Port = p
	}
	if c.From == "" {
		c.From = c.User
	}
	return c, c.Host != "" && c.From != ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sendEmail(ctx context.Context, to []string, subject, body string) error {
	cfg, ok := smtpFromEnv()
	if !ok {
		return errors.New("email needs VISIONARY_SMTP_HOST and VISIONARY_SMTP_FROM")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return errors.New("VISIONARY_SMTP_FROM is not an email address")
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if err := c.Hello("visionary"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if cfg.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else if !isLoopback(cfg.Host) {
			return errors.New("smtp server does not offer STARTTLS")
		}
	}
	if cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp recipient: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(buildMessage(from.String(), to, subject, body)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	return c.Quit()
}

func buildMessage(from string, to []string, subject, body string) []byte {
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	host := "visionary.local"
	if a, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(a.Address, "@"); at >= 0 {
			host = a.Address[at+1:]
		}
	}
	// Header values are single-line: strip CR/LF so a rule name can never add
	// headers.
	clean := func(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
	var b strings.Builder
	b.WriteString("From: " + clean(from) + "\r\n")
	b.WriteString("To: " + clean(strings.Join(to, ", ")) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", clean(subject)) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + hex.EncodeToString(id) + "@" + host + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	// The SMTP data writer dot-stuffs lines itself.
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		b.WriteString(line + "\r\n")
	}
	return []byte(b.String())
}
