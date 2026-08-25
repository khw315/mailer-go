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

const (
	headerContentType             = "Content-Type"
	headerContentTransferEncoding = "Content-Transfer-Encoding"
	headerContentDisposition      = "Content-Disposition"
	mimeTextPlainUTF8             = "text/plain; charset=UTF-8"
	mimeTextHTMLUTF8              = "text/html; charset=UTF-8"
	encoding8bit                  = "8bit"
	encodingBase64                = "base64"
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
	if err := validateSendRequest(req); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writeStandardHeaders(&buf, req)

	hasHTML := strings.TrimSpace(req.HTML) != ""
	hasText := strings.TrimSpace(req.Text) != ""
	hasAttachments := len(req.Attachments) > 0

	if !hasAttachments {
		if hasText && !hasHTML {
			return appendSinglePart(&buf, mimeTextPlainUTF8, req.Text), nil
		}
		if hasHTML && !hasText {
			return appendSinglePart(&buf, mimeTextHTMLUTF8, req.HTML), nil
		}
		return buildMultipartAlternative(&buf, req.Text, req.HTML)
	}

	return buildMultipartMixed(&buf, req, hasText, hasHTML)
}

func validateSendRequest(req *SendRequest) error {
	if req.From == "" {
		return fmt.Errorf("sender 'from' is required")
	}
	if len(req.To) == 0 && len(req.Cc) == 0 && len(req.Bcc) == 0 {
		return fmt.Errorf("at least one recipient ('to', 'cc', or 'bcc') is required")
	}
	if req.Subject == "" {
		req.Subject = "(no subject)"
	}
	return nil
}

func writeStandardHeaders(buf *bytes.Buffer, req *SendRequest) {
	fmt.Fprintf(buf, "From: %s\r\n", req.From)
	fmt.Fprintf(buf, "To: %s\r\n", strings.Join(req.To, ", "))
	if len(req.Cc) > 0 {
		fmt.Fprintf(buf, "Cc: %s\r\n", strings.Join(req.Cc, ", "))
	}
	if req.ReplyTo != "" {
		fmt.Fprintf(buf, "Reply-To: %s\r\n", req.ReplyTo)
	}
	fmt.Fprintf(buf, "Subject: %s\r\n", req.Subject)
	fmt.Fprintf(buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(buf, "Message-ID: <%d.%s@mailer-go>\r\n", time.Now().UnixNano(), req.From)
	fmt.Fprintf(buf, "MIME-Version: 1.0\r\n")

	for k, v := range req.Headers {
		fmt.Fprintf(buf, "%s: %s\r\n", k, v)
	}
}

func appendSinglePart(buf *bytes.Buffer, contentType, body string) []byte {
	fmt.Fprintf(buf, "%s: %s\r\n", headerContentType, contentType)
	fmt.Fprintf(buf, "%s: %s\r\n\r\n", headerContentTransferEncoding, encoding8bit)
	buf.WriteString(body)
	return buf.Bytes()
}

func buildMultipartMixed(buf *bytes.Buffer, req *SendRequest, hasText, hasHTML bool) ([]byte, error) {
	mixedWriter := multipart.NewWriter(buf)
	fmt.Fprintf(buf, "%s: multipart/mixed; boundary=\"%s\"\r\n\r\n", headerContentType, mixedWriter.Boundary())

	if hasText && hasHTML {
		if err := writeAlternativeContainer(buf, mixedWriter.Boundary(), req.Text, req.HTML); err != nil {
			return nil, err
		}
	} else if hasHTML {
		if err := writeSingleBodyPart(mixedWriter, mimeTextHTMLUTF8, req.HTML); err != nil {
			return nil, err
		}
	} else {
		if err := writeSingleBodyPart(mixedWriter, mimeTextPlainUTF8, req.Text); err != nil {
			return nil, err
		}
	}

	if err := writeAttachments(mixedWriter, req.Attachments); err != nil {
		return nil, err
	}

	if err := mixedWriter.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeAlternativeContainer(buf *bytes.Buffer, mixedBoundary, text, html string) error {
	altBuf := &bytes.Buffer{}
	altWriter := multipart.NewWriter(altBuf)
	fmt.Fprintf(buf, "--%s\r\n", mixedBoundary)
	fmt.Fprintf(buf, "%s: multipart/alternative; boundary=\"%s\"\r\n\r\n", headerContentType, altWriter.Boundary())

	if err := writeSingleBodyPart(altWriter, mimeTextPlainUTF8, text); err != nil {
		return err
	}
	if err := writeSingleBodyPart(altWriter, mimeTextHTMLUTF8, html); err != nil {
		return err
	}

	if err := altWriter.Close(); err != nil {
		return err
	}
	buf.Write(altBuf.Bytes())
	return nil
}

func writeSingleBodyPart(w *multipart.Writer, contentType, body string) error {
	h := make(textproto.MIMEHeader)
	h.Set(headerContentType, contentType)
	h.Set(headerContentTransferEncoding, encoding8bit)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(body))
	return err
}

func writeAttachments(w *multipart.Writer, attachments []Attachment) error {
	for _, att := range attachments {
		cType := att.ContentType
		if cType == "" {
			cType = "application/octet-stream"
		}
		attHeader := make(textproto.MIMEHeader)
		attHeader.Set(headerContentType, fmt.Sprintf("%s; name=\"%s\"", cType, att.Filename))
		attHeader.Set(headerContentDisposition, fmt.Sprintf("attachment; filename=\"%s\"", att.Filename))
		attHeader.Set(headerContentTransferEncoding, encodingBase64)

		part, err := w.CreatePart(attHeader)
		if err != nil {
			return err
		}

		decoded, err := base64.StdEncoding.DecodeString(att.Base64Data)
		if err != nil {
			return fmt.Errorf("invalid base64 in attachment %s: %w", att.Filename, err)
		}

		encoded := base64.StdEncoding.EncodeToString(decoded)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			if _, err := part.Write([]byte(encoded[i:end] + "\r\n")); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildMultipartAlternative(buf *bytes.Buffer, text, html string) ([]byte, error) {
	altWriter := multipart.NewWriter(buf)
	fmt.Fprintf(buf, "%s: multipart/alternative; boundary=\"%s\"\r\n\r\n", headerContentType, altWriter.Boundary())

	if err := writeSingleBodyPart(altWriter, mimeTextPlainUTF8, text); err != nil {
		return nil, err
	}
	if err := writeSingleBodyPart(altWriter, mimeTextHTMLUTF8, html); err != nil {
		return nil, err
	}

	if err := altWriter.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
