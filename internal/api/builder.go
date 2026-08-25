package api

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
)

// SendRequest represents the JSON payload for the HTTP email submission API (/v1/send).
type SendRequest struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	Cc          []string          `json:"cc,omitempty"`
	Bcc         []string          `json:"bcc,omitempty"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Subject     string            `json:"subject"`
	Text        string            `json:"text,omitempty"`
	HTML        string            `json:"html,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Async       bool              `json:"async,omitempty"` // Force queuing even if synchronous relay is requested
}

// Attachment represents an email file attachment.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Base64Data  string `json:"data"` // base64-encoded file content
}

// BuildMIME converts a SendRequest into standard RFC 5322/2046 MIME message bytes.
func BuildMIME(req *SendRequest) ([]byte, error) {
	if req.From == "" {
		return nil, fmt.Errorf("sender 'from' is required")
	}
	if len(req.To) == 0 && len(req.Cc) == 0 && len(req.Bcc) == 0 {
		return nil, fmt.Errorf("at least one recipient ('to', 'cc', or 'bcc') is required")
	}
	if req.Subject == "" {
		req.Subject = "(no subject)"
	}

	var buf bytes.Buffer

	// Standard Headers
	fmt.Fprintf(&buf, "From: %s\r\n", req.From)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(req.To, ", "))
	if len(req.Cc) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(req.Cc, ", "))
	}
	if req.ReplyTo != "" {
		fmt.Fprintf(&buf, "Reply-To: %s\r\n", req.ReplyTo)
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", req.Subject)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: <%d.%s@mailer-go>\r\n", time.Now().UnixNano(), req.From)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")

	// Custom Headers
	for k, v := range req.Headers {
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}

	hasHTML := strings.TrimSpace(req.HTML) != ""
	hasText := strings.TrimSpace(req.Text) != ""
	hasAttachments := len(req.Attachments) > 0

	// Case 1: Plain text only without attachments
	if hasText && !hasHTML && !hasAttachments {
		fmt.Fprintf(&buf, "Content-Type: text/plain; charset=UTF-8\r\n")
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(req.Text)
		return buf.Bytes(), nil
	}

	// Case 2: HTML only without attachments
	if hasHTML && !hasText && !hasAttachments {
		fmt.Fprintf(&buf, "Content-Type: text/html; charset=UTF-8\r\n")
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(req.HTML)
		return buf.Bytes(), nil
	}

	// Multipart mixed (for attachments) or alternative (for text + HTML)
	if hasAttachments {
		mixedWriter := multipart.NewWriter(&buf)
		fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", mixedWriter.Boundary())

		// Body container (alternative or plain/html)
		if hasText && hasHTML {
			altBuf := &bytes.Buffer{}
			altWriter := multipart.NewWriter(altBuf)
			fmt.Fprintf(&buf, "--%s\r\n", mixedWriter.Boundary())
			fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altWriter.Boundary())

			// Text part
			textHeader := make(textproto.MIMEHeader)
			textHeader.Set("Content-Type", "text/plain; charset=UTF-8")
			textHeader.Set("Content-Transfer-Encoding", "8bit")
			part, _ := altWriter.CreatePart(textHeader)
			_, _ = part.Write([]byte(req.Text))

			// HTML part
			htmlHeader := make(textproto.MIMEHeader)
			htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
			htmlHeader.Set("Content-Transfer-Encoding", "8bit")
			partHTML, _ := altWriter.CreatePart(htmlHeader)
			_, _ = partHTML.Write([]byte(req.HTML))

			_ = altWriter.Close()
			buf.Write(altBuf.Bytes())
		} else if hasHTML {
			htmlHeader := make(textproto.MIMEHeader)
			htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
			htmlHeader.Set("Content-Transfer-Encoding", "8bit")
			part, _ := mixedWriter.CreatePart(htmlHeader)
			_, _ = part.Write([]byte(req.HTML))
		} else {
			textHeader := make(textproto.MIMEHeader)
			textHeader.Set("Content-Type", "text/plain; charset=UTF-8")
			textHeader.Set("Content-Transfer-Encoding", "8bit")
			part, _ := mixedWriter.CreatePart(textHeader)
			_, _ = part.Write([]byte(req.Text))
		}

		// Attachments
		for _, att := range req.Attachments {
			cType := att.ContentType
			if cType == "" {
				cType = "application/octet-stream"
			}
			attHeader := make(textproto.MIMEHeader)
			attHeader.Set("Content-Type", fmt.Sprintf("%s; name=\"%s\"", cType, att.Filename))
			attHeader.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", att.Filename))
			attHeader.Set("Content-Transfer-Encoding", "base64")

			part, err := mixedWriter.CreatePart(attHeader)
			if err != nil {
				return nil, err
			}

			// Validate and write base64
			decoded, err := base64.StdEncoding.DecodeString(att.Base64Data)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 in attachment %s: %w", att.Filename, err)
			}
			encoded := base64.StdEncoding.EncodeToString(decoded)
			for i := 0; i < len(encoded); i += 76 {
				end := i + 76
				if end > len(encoded) {
					end = len(encoded)
				}
				_, _ = part.Write([]byte(encoded[i:end] + "\r\n"))
			}
		}

		_ = mixedWriter.Close()
		return buf.Bytes(), nil
	}

	// Multipart Alternative (Text + HTML without attachments)
	altWriter := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altWriter.Boundary())

	// Plain text part
	textHeader := make(textproto.MIMEHeader)
	textHeader.Set("Content-Type", "text/plain; charset=UTF-8")
	textHeader.Set("Content-Transfer-Encoding", "8bit")
	textPart, _ := altWriter.CreatePart(textHeader)
	_, _ = textPart.Write([]byte(req.Text))

	// HTML part
	htmlHeader := make(textproto.MIMEHeader)
	htmlHeader.Set("Content-Type", "text/html; charset=UTF-8")
	htmlHeader.Set("Content-Transfer-Encoding", "8bit")
	htmlPart, _ := altWriter.CreatePart(htmlHeader)
	_, _ = htmlPart.Write([]byte(req.HTML))

	_ = altWriter.Close()
	return buf.Bytes(), nil
}
