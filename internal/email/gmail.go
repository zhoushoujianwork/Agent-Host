package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type gmailProvider struct {
	replyThreadID string
	account       Account
	token         string
	request       func(context.Context, string, string, string, []byte, bool, any) error
}
type gmailCredential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func httpJSON(ctx context.Context, method, address, token string, body []byte, form bool, out any) error {
	req, e := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if e != nil {
		return errors.New("invalid mail API request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if form {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("mail API network failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mail API HTTP %d", resp.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if e != nil || len(raw) >= 12<<20 {
		return errors.New("mail API response exceeds limit or cannot be read")
	}
	if json.Unmarshal(raw, out) != nil {
		return errors.New("invalid mail API response")
	}
	return nil
}
func (p *gmailProvider) requestJSON(ctx context.Context, method, address, token string, body []byte, form bool, out any) error {
	f := p.request
	if f == nil {
		f = httpJSON
	}
	return f(ctx, method, address, token, body, form, out)
}

func (p *gmailProvider) authorize(ctx context.Context) error {
	if p.token != "" {
		return nil
	}
	raw, e := privateFile(p.account.CredentialFile)
	if e != nil {
		return e
	}
	var cred gmailCredential
	if json.Unmarshal(raw, &cred) != nil || cred.ClientID == "" || cred.ClientSecret == "" || cred.RefreshToken == "" {
		return errors.New("Gmail requires private OAuth client_id, client_secret and refresh_token")
	}
	values := url.Values{"client_id": {cred.ClientID}, "client_secret": {cred.ClientSecret}, "refresh_token": {cred.RefreshToken}, "grant_type": {"refresh_token"}}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if e = p.requestJSON(ctx, "POST", "https://oauth2.googleapis.com/token", "", []byte(values.Encode()), true, &token); e != nil {
		return e
	}
	if token.AccessToken == "" {
		return errors.New("Gmail OAuth returned no access token")
	}
	var profile struct {
		Address string `json:"emailAddress"`
	}
	if e = p.requestJSON(ctx, "GET", "https://gmail.googleapis.com/gmail/v1/users/me/profile", token.AccessToken, nil, false, &profile); e != nil {
		return e
	}
	if !strings.EqualFold(profile.Address, p.account.Address) {
		return errors.New("Gmail credential identity does not match configured account")
	}
	p.token = token.AccessToken
	return nil
}
func (p *gmailProvider) api(ctx context.Context, method, path string, body []byte, out any) error {
	if e := p.authorize(ctx); e != nil {
		return e
	}
	return p.requestJSON(ctx, method, "https://gmail.googleapis.com/gmail/v1/users/me/"+path, p.token, body, false, out)
}
func (p *gmailProvider) Check(ctx context.Context) (any, error) {
	if e := p.authorize(ctx); e != nil {
		return nil, e
	}
	return map[string]any{"account_id": p.account.ID, "gmail": "authenticated", "checked_at": time.Now().UTC(), "sending": "unchecked"}, nil
}
func (p *gmailProvider) Folders(ctx context.Context) (any, error) {
	var out struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	e := p.api(ctx, "GET", "labels", nil, &out)
	return map[string]any{"account_id": p.account.ID, "labels": out.Labels}, e
}
func (p *gmailProvider) Search(ctx context.Context, a Args) (any, error) {
	query := a.Query
	quote := func(s string) string {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	if a.From != "" {
		query += " from:" + quote(a.From)
	}
	if a.Subject != "" {
		query += " subject:" + quote(a.Subject)
	}
	if a.Unread {
		query += " is:unread"
	}
	if a.Since != "" {
		if _, e := time.Parse("2006-01-02", a.Since); e != nil {
			return nil, errors.New("since must be YYYY-MM-DD")
		}
		query += " after:" + a.Since
	}
	v := url.Values{"q": {query}, "maxResults": {strconv.Itoa(a.Limit)}, "labelIds": {a.Folder}}
	if a.Cursor != "" {
		v.Set("pageToken", a.Cursor)
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Next string `json:"nextPageToken"`
	}
	if e := p.api(ctx, "GET", "messages?"+v.Encode(), nil, &list); e != nil {
		return nil, e
	}
	rows := []map[string]any{}
	for _, m := range list.Messages {
		var detail struct {
			ID      string `json:"id"`
			Snippet string `json:"snippet"`
			Payload struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		}
		if e := p.api(ctx, "GET", "messages/"+url.PathEscape(m.ID)+"?format=metadata", nil, &detail); e != nil {
			return nil, e
		}
		rows = append(rows, map[string]any{"message_id": encodeRef(Ref{Account: a.AccountID, GmailID: m.ID}), "headers": detail.Payload.Headers, "snippet": detail.Snippet})
	}
	return map[string]any{"account_id": a.AccountID, "messages": rows, "next_cursor": list.Next, "coverage": "specified Gmail label and query only"}, nil
}
func (p *gmailProvider) Raw(ctx context.Context, r Ref) ([]byte, error) {
	if r.Account != p.account.ID || r.GmailID == "" || r.UID != 0 {
		return nil, errors.New("invalid Gmail message reference")
	}
	var out struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if e := p.api(ctx, "GET", "messages/"+url.PathEscape(r.GmailID)+"?format=raw", nil, &out); e != nil {
		return nil, e
	}
	p.replyThreadID = out.ThreadID
	data, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(out.Raw, "="))
	if e != nil || len(data) > maxMessageBytes {
		return nil, errors.New("invalid or oversized Gmail message")
	}
	return data, nil
}
func (p *gmailProvider) Send(ctx context.Context, raw []byte, to []string) (string, error) {
	message := map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}
	if p.replyThreadID != "" {
		message["threadId"] = p.replyThreadID
	}
	body, _ := json.Marshal(message)
	var out struct {
		ID string `json:"id"`
	}
	if e := p.api(ctx, "POST", "messages/send", body, &out); e != nil {
		return "", e
	}
	if out.ID == "" {
		return "", errors.New("Gmail returned no send receipt")
	}
	return out.ID, nil
}
