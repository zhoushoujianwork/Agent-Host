package email

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/emersion/go-message/charset"
	messageMail "github.com/emersion/go-message/mail"
)

type attachment struct {
	ID, Filename, ContentType string
	Data                      []byte
}
type parsedMail struct {
	Headers     messageMail.Header
	Body        string
	Attachments []attachment
}

func parseMIME(raw []byte) (parsedMail, error) {
	var out parsedMail
	r, e := messageMail.CreateReader(bytes.NewReader(raw))
	if e != nil {
		return out, errors.New("cannot decode mail MIME")
	}
	defer r.Close()
	out.Headers = r.Header
	var text, html strings.Builder
	for index := 0; ; index++ {
		part, e := r.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, errors.New("cannot decode MIME part")
		}
		if index >= 200 {
			return out, errors.New("mail exceeds 200 MIME parts")
		}
		body, e := io.ReadAll(io.LimitReader(part.Body, maxMessageBytes+1))
		if e != nil || len(body) > maxMessageBytes {
			return out, errors.New("MIME part exceeds limit or cannot be read")
		}
		switch h := part.Header.(type) {
		case *messageMail.AttachmentHeader:
			filename, e := h.Filename()
			if e != nil {
				return out, errors.New("invalid attachment filename")
			}
			ct, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
			out.Attachments = append(out.Attachments, attachment{strconv.Itoa(index), filename, ct, body})
		case *messageMail.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/plain" {
				text.Write(body)
				text.WriteByte('\n')
			} else if ct == "text/html" {
				html.Write(body)
				html.WriteByte('\n')
			} else {
				out.Attachments = append(out.Attachments, attachment{strconv.Itoa(index), "inline-" + strconv.Itoa(index), ct, body})
			}
		}
	}
	out.Body = text.String()
	if out.Body == "" {
		out.Body = html.String()
	}
	out.Body = strings.ToValidUTF8(out.Body, "�")
	return out, nil
}
func readMIME(raw []byte, a Args, wantAttachment bool) (any, error) {
	length := a.Length
	if length == 0 {
		length = maxChunk
	}
	if length < 1 || length > maxChunk {
		return nil, errors.New("invalid read length")
	}
	p, e := parseMIME(raw)
	if e != nil {
		return nil, e
	}
	hash := sha256.Sum256(raw)
	result := map[string]any{"account_id": a.AccountID, "message_id": a.MessageID, "source_sha256": hex.EncodeToString(hash[:]), "source_kind": "untrusted_email"}
	if wantAttachment {
		for _, att := range p.Attachments {
			if att.ID != a.AttachmentID {
				continue
			}
			if a.Offset > len(att.Data) {
				return nil, errors.New("attachment offset exceeds size")
			}
			end := min(a.Offset+length, len(att.Data))
			digest := sha256.Sum256(att.Data)
			result["data_base64"] = base64.StdEncoding.EncodeToString(att.Data[a.Offset:end])
			result["filename"] = att.Filename
			result["content_type"] = att.ContentType
			result["size"] = len(att.Data)
			result["sha256"] = hex.EncodeToString(digest[:])
			result["next_offset"] = end
			result["complete"] = end == len(att.Data)
			return result, nil
		}
		return nil, errors.New("unknown attachment_id")
	}
	if a.Offset > len(p.Body) || a.Offset < len(p.Body) && !utf8.RuneStart(p.Body[a.Offset]) {
		return nil, errors.New("invalid body offset")
	}
	end := min(a.Offset+length, len(p.Body))
	for end < len(p.Body) && !utf8.RuneStart(p.Body[end]) {
		end--
	}
	if end == a.Offset && end < len(p.Body) {
		return nil, errors.New("length is too small for next UTF-8 character")
	}
	subject, _ := p.Headers.Subject()
	result["subject"] = subject
	headers := map[string]string{}
	for _, k := range []string{"From", "To", "Cc", "Reply-To", "Date", "Message-ID", "In-Reply-To", "References"} {
		headers[k] = p.Headers.Get(k)
	}
	result["headers"] = headers
	result["body"] = p.Body[a.Offset:end]
	result["next_offset"] = end
	result["complete"] = end == len(p.Body)
	result["body_bytes"] = len(p.Body)
	atts := []map[string]any{}
	for _, att := range p.Attachments {
		atts = append(atts, map[string]any{"attachment_id": att.ID, "filename": att.Filename, "content_type": att.ContentType, "size": len(att.Data)})
	}
	result["attachments"] = atts
	return result, nil
}
func composeMessage(account Account, a Args, original []byte) ([]byte, []string, string, error) {
	if len(a.To) == 0 || len(a.To) > 50 || strings.TrimSpace(a.Body) == "" || len(a.Body) > 128<<10 || strings.ContainsAny(a.Subject, "\r\n\x00") || len(a.Subject) > 998 {
		return nil, nil, "", errors.New("invalid recipients, subject or body")
	}
	var h messageMail.Header
	h.SetAddressList("From", []*mail.Address{{Address: account.Address}})
	recipients := []*mail.Address{}
	envelope := []string{}
	for _, s := range a.To {
		if strings.ContainsAny(s, "\r\n\x00") {
			return nil, nil, "", errors.New("invalid recipient")
		}
		addr, e := mail.ParseAddress(s)
		if e != nil {
			return nil, nil, "", errors.New("invalid recipient")
		}
		recipients = append(recipients, addr)
		envelope = append(envelope, addr.Address)
	}
	h.SetAddressList("To", recipients)
	h.SetDate(time.Now())
	if e := h.GenerateMessageIDWithHostname(strings.Split(account.Address, "@")[1]); e != nil {
		return nil, nil, "", e
	}
	h.SetSubject(a.Subject)
	if original != nil {
		p, e := parseMIME(original)
		if e != nil {
			return nil, nil, "", e
		}
		id, e := p.Headers.MessageID()
		if e != nil || id == "" {
			return nil, nil, "", errors.New("original mail has no valid Message-ID for reply")
		}
		refs, _ := p.Headers.MsgIDList("References")
		if len(refs) > 20 {
			refs = refs[len(refs)-20:]
		}
		h.SetMsgIDList("References", append(refs, id))
		h.SetMsgIDList("In-Reply-To", []string{id})
		subject, _ := p.Headers.Subject()
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}
		if len(subject) > 998 || strings.ContainsAny(subject, "\r\n") {
			return nil, nil, "", errors.New("invalid original subject")
		}
		h.SetSubject(subject)
	}
	var buf bytes.Buffer
	w, e := messageMail.CreateWriter(&buf, h)
	if e != nil {
		return nil, nil, "", e
	}
	var ih messageMail.InlineHeader
	ih.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	part, e := w.CreateSingleInline(ih)
	if e != nil {
		return nil, nil, "", e
	}
	if _, e = io.WriteString(part, a.Body); e != nil {
		return nil, nil, "", e
	}
	if e = part.Close(); e != nil {
		return nil, nil, "", e
	}
	total := 0
	if len(a.Attachments) > 10 {
		return nil, nil, "", errors.New("too many attachments")
	}
	for _, att := range a.Attachments {
		data, e := base64.StdEncoding.DecodeString(att.Data)
		total += len(data)
		if e != nil || total > 512<<10 || att.Filename == "" || len(att.Filename) > 255 || strings.ContainsAny(att.Filename, "/\\\r\n\x00") {
			return nil, nil, "", errors.New("invalid attachment or 512 KiB total limit exceeded")
		}
		ct, params, e := mime.ParseMediaType(att.ContentType)
		if e != nil {
			return nil, nil, "", errors.New("invalid attachment content_type")
		}
		var ah messageMail.AttachmentHeader
		ah.SetContentType(ct, params)
		ah.SetFilename(att.Filename)
		part, e := w.CreateAttachment(ah)
		if e != nil {
			return nil, nil, "", e
		}
		if _, e = part.Write(data); e != nil {
			return nil, nil, "", e
		}
		if e = part.Close(); e != nil {
			return nil, nil, "", e
		}
	}
	if e = w.Close(); e != nil {
		return nil, nil, "", e
	}
	id, _ := h.MessageID()
	if buf.Len() > 1<<20 {
		return nil, nil, "", fmt.Errorf("outgoing MIME exceeds 1 MiB")
	}
	return buf.Bytes(), envelope, id, nil
}
