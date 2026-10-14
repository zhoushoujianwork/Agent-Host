package dingtalk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tinyMediaPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func mediaHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestMediaDownloaderResolvesAuthenticatedCapabilitiesAndCachesToken(t *testing.T) {
	image, _ := base64.StdEncoding.DecodeString(tinyMediaPNG)
	var tokenCalls atomic.Int32
	var mediaCalls atomic.Int32
	config := testConfig
	config.RobotCode = "application-robot"
	downloader := &MediaDownloader{Config: config, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > mediaTimeout {
			t.Error("media operation is missing its timeout")
		}
		switch req.URL.Path {
		case reactionTokenPath:
			tokenCalls.Add(1)
			return mediaHTTPResponse(`{"accessToken":"TOKEN_SECRET","expireIn":7200}`), nil
		case messageFileDownloadPath:
			var body map[string]string
			if json.NewDecoder(req.Body).Decode(&body) != nil || body["downloadCode"] != "CODE_SECRET" || body["robotCode"] != "application-robot" || req.Header.Get("x-acs-dingtalk-access-token") != "TOKEN_SECRET" {
				t.Error("attachment resolution lost trusted credentials")
			}
			return mediaHTTPResponse(`{"downloadUrl":"https://media.aliyuncs.com/media?token=CDN_SECRET"}`), nil
		case "/media":
			mediaCalls.Add(1)
			if req.URL.Host != "media.aliyuncs.com" || req.Header.Get("x-acs-dingtalk-access-token") != "" {
				t.Error("CDN requested at untrusted host or received app token")
			}
			return mediaHTTPResponse(string(image)), nil
		default:
			t.Error("unexpected media request")
			return nil, errors.New("unexpected request")
		}
	})}}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, mime, err := downloader.Download(context.Background(), Attachment{Kind: "image", DownloadCode: "CODE_SECRET"})
			if err != nil || string(data) != string(image) || mime != "image/png" {
				t.Errorf("image download failed: mime=%q error=%v", mime, err)
			}
		}()
	}
	wg.Wait()
	if tokenCalls.Load() != 1 || mediaCalls.Load() != 4 {
		t.Fatalf("token/media calls = %d/%d", tokenCalls.Load(), mediaCalls.Load())
	}
}

func TestMediaDownloaderFilesAndFailuresRemainExplicit(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		length           int64
		status           int
		readError        bool
		wantOK           bool
	}{
		{name: "text attachment", kind: "file", body: "synthetic error log", wantOK: true},
		{name: "PDF attachment", kind: "file", body: "%PDF-1.7\nsynthetic document", wantOK: true},
		{name: "empty", kind: "file"},
		{name: "unsupported image", kind: "image", body: "not an image"},
		{name: "advertised oversized", kind: "file", body: "small", length: MaxFileBytes + 1},
		{name: "stream oversized", kind: "image", body: strings.Repeat("x", MaxPictureBytes+1)},
		{name: "rejected", kind: "file", body: "SECRET", status: 403},
		{name: "partial read", kind: "file", body: "small", readError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &MediaDownloader{Config: testConfig, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case reactionTokenPath:
					return mediaHTTPResponse(`{"accessToken":"TOKEN_SECRET","expireIn":7200}`), nil
				case messageFileDownloadPath:
					return mediaHTTPResponse(`{"downloadUrl":"https://media.aliyuncs.com/file?secret=CDN_SECRET"}`), nil
				default:
					response := mediaHTTPResponse(tc.body)
					response.ContentLength = tc.length
					if tc.status != 0 {
						response.StatusCode = tc.status
					}
					if tc.readError {
						response.Body = io.NopCloser(mediaErrorReader{})
					}
					return response, nil
				}
			})}}
			data, mime, err := d.Download(context.Background(), Attachment{Kind: tc.kind, DownloadCode: "CODE_SECRET"})
			if tc.wantOK {
				if err != nil || string(data) != tc.body || mime == "" {
					t.Fatalf("file download failed: %v", err)
				}
			} else if err == nil || data != nil || mime != "" || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("media failure swallowed or disclosed secrets: %v", err)
			}
		})
	}
}

type mediaErrorReader struct{}

func (mediaErrorReader) Read([]byte) (int, error) { return 0, errors.New("CDN_SECRET") }

func TestMediaURLAndRedirectValidation(t *testing.T) {
	for _, target := range []string{
		"https://127.0.0.1/file", "https://[::1]/file", "https://localhost/file", "http://media.aliyuncs.com/file",
		"https://media.aliyuncs.com.evil.test/file", "https://evilaliyuncs.com/file", "https://media.aliyuncs.com:443/file",
		"https://user@media.aliyuncs.com/file", "https://media.aliyuncs.com/file#fragment", "file:///private/secret",
		"http://127.0.0.1/file", "http://media.aliyuncs.com.evil.test/file", "http://evilaliyuncs.com/file",
		"http://media.aliyuncs.com:80/file", "http://user@media.aliyuncs.com/file", "http://media.aliyuncs.com/file#fragment",
	} {
		if trustedMediaURL(target) {
			t.Errorf("unsafe download URL accepted: %s", target)
		}
		if target != "http://media.aliyuncs.com/file" {
			if _, ok := secureMediaURL(target); ok {
				t.Errorf("unsafe download URL normalized: %s", target)
			}
		}
	}
	for _, target := range []string{"https://media.aliyuncs.com/file", "https://media.alicdn.com/file", "https://media.dingtalkcdn.com/file", "https://dingtalk.com/file"} {
		if !trustedMediaURL(target) {
			t.Errorf("trusted download host rejected: %s", target)
		}
	}
	for _, stage := range []string{"resolver", "redirect", "transport", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			var requests atomic.Int32
			d := &MediaDownloader{Config: testConfig, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				if req.URL.Host == "attacker.example" {
					t.Fatal("media operation fetched arbitrary URL")
				}
				switch req.URL.Path {
				case reactionTokenPath:
					return mediaHTTPResponse(`{"accessToken":"TOKEN_SECRET","expireIn":7200}`), nil
				case messageFileDownloadPath:
					if stage == "resolver" {
						return mediaHTTPResponse(`{"downloadUrl":"https://attacker.example/CODE_SECRET"}`), nil
					}
					return mediaHTTPResponse(`{"downloadUrl":"https://media.aliyuncs.com/file?secret=CDN_SECRET"}`), nil
				default:
					if stage == "transport" {
						return nil, errors.New("CDN_SECRET")
					}
					response := mediaHTTPResponse("")
					response.StatusCode = http.StatusFound
					response.Header.Set("Location", "https://attacker.example/CDN_SECRET")
					return response, nil
				}
			})}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "cancel" {
				cancel()
			}
			data, mime, err := d.Download(ctx, Attachment{Kind: "file", DownloadCode: "CODE_SECRET"})
			if err == nil || data != nil || mime != "" || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "http") {
				t.Fatalf("bad media request error: %v", err)
			}
			if stage == "cancel" && (!errors.Is(err, context.Canceled) || requests.Load() != 0) {
				t.Fatalf("canceled request dispatched: %d, %v", requests.Load(), err)
			}
		})
	}
}

func TestMediaDownloaderUpgradesSignedHTTPURLsBeforeEveryFetch(t *testing.T) {
	image, _ := base64.StdEncoding.DecodeString(tinyMediaPNG)
	const signedPath = "/folder/image%2F1.png?Expires=123&Signature=CDN_SECRET%2B%2F%3D&OSSAccessKeyId=KEY_SECRET"
	const mediaHost = "wukong-file-im-sz.oss-cn-shenzhen.aliyuncs.com"
	for _, scheme := range []string{"http", "https"} {
		for _, redirect := range []string{"none", "trusted HTTP", "untrusted HTTP", "HTTP with port", "redirect loop"} {
			t.Run(scheme+"/"+redirect, func(t *testing.T) {
				var mediaCalls int
				downloader := &MediaDownloader{Config: testConfig, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Scheme != "https" {
						t.Fatal("download capability was sent over HTTP")
					}
					switch req.URL.Path {
					case reactionTokenPath:
						return mediaHTTPResponse(`{"accessToken":"TOKEN_SECRET","expireIn":7200}`), nil
					case messageFileDownloadPath:
						return mediaHTTPResponse(`{"downloadUrl":"` + scheme + `://` + mediaHost + signedPath + `"}`), nil
					default:
						mediaCalls++
						if req.URL.Host != mediaHost || req.Header.Get("x-acs-dingtalk-access-token") != "" || req.Header.Get("Authorization") != "" {
							t.Fatal("media fetched from an untrusted host or received application credentials")
						}
						if req.URL.RequestURI() != signedPath {
							t.Fatal("signed media path or query changed")
						}
						if mediaCalls == 1 && redirect != "none" || redirect == "redirect loop" {
							response := mediaHTTPResponse("")
							response.StatusCode = http.StatusFound
							location := "http://" + mediaHost + signedPath
							if redirect == "untrusted HTTP" {
								location = "http://attacker.example/CDN_SECRET"
							} else if redirect == "HTTP with port" {
								location = "http://" + mediaHost + ":80" + signedPath
							}
							response.Header.Set("Location", location)
							return response, nil
						}
						return mediaHTTPResponse(string(image)), nil
					}
				})}}
				data, mime, err := downloader.Download(context.Background(), Attachment{Kind: "image", DownloadCode: "CODE_SECRET"})
				if redirect == "untrusted HTTP" || redirect == "HTTP with port" || redirect == "redirect loop" {
					wantCalls := 1
					if redirect == "redirect loop" {
						wantCalls = 5
					}
					if err == nil || data != nil || mime != "" || mediaCalls != wantCalls || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "http") {
						t.Fatalf("unsafe redirect was fetched or disclosed: calls=%d error=%v", mediaCalls, err)
					}
					return
				}
				wantCalls := 1
				if redirect != "none" {
					wantCalls = 2
				}
				if err != nil || string(data) != string(image) || mime != "image/png" || mediaCalls != wantCalls {
					t.Fatalf("secure download failed: calls=%d mime=%q error=%v", mediaCalls, mime, err)
				}
			})
		}
	}
}
