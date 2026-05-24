package mail

import (
	"fmt"
	"net/smtp"
	"strings"
)

func BookingConfirmation(host string, port int, user, password, fromAddr, toUser string, subject, body string) error {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return fmt.Errorf("SMTP_HOST / SMTP_PORT not configured")
	}
	fromAddr = strings.TrimSpace(fromAddr)
	toUser = strings.TrimSpace(toUser)
	if fromAddr == "" || toUser == "" {
		return fmt.Errorf("SMTP_FROM or user email missing")
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(nil); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	auth := smtp.PlainAuth("", user, password, host)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	if err := c.Mail(extractEmail(fromAddr)); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(toUser); err != nil {
		return fmt.Errorf("rcpt: %w", err)
	}

	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		fromAddr, toUser, subject, body,
	)
	if _, err := fmt.Fprint(wc, msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return c.Quit()
}

func extractEmail(addr string) string {
    if i := strings.Index(addr, "<"); i >= 0 {
        return strings.TrimSuffix(strings.TrimSpace(addr[i+1:]), ">")
    }
    return addr
}
