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
	auth := smtp.PlainAuth("", user, password, host)
	msg := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		fromAddr, toUser, subject, body,
	))
	return smtp.SendMail(addr, auth, extractEmail(fromAddr), []string{toUser}, msg)
}

func extractEmail(addr string) string {
    if i := strings.Index(addr, "<"); i >= 0 {
        return strings.TrimSuffix(strings.TrimSpace(addr[i+1:]), ">")
    }
    return addr
}
