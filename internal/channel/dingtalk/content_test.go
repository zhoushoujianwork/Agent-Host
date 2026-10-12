package dingtalk

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseMediaKeepsOrderedTextAndPrivateCapabilities(t *testing.T) {
	for _, kind := range []string{"picture", "file", "richText"} {
		t.Run(kind, func(t *testing.T) {
			message, err := Parse(callbackPayload(t, func(m map[string]any) {
				m["msgtype"] = kind
				m["conversationType"], m["isInAtList"] = "2", true
				m["senderStaffId"] = "colleague"
				switch kind {
				case "picture":
					m["content"] = map[string]any{"downloadCode": "PICTURE_CAPABILITY"}
				case "file":
					m["content"] = map[string]any{"downloadCode": "FILE_CAPABILITY", "fileName": "error.log", "fileSize": 2048, "fileType": "log"}
				case "richText":
					m["content"] = map[string]any{"richText": []any{
						map[string]any{"text": "@Helper inspect "},
						map[string]any{"type": "picture", "pictureDownloadCode": "PICTURE_CAPABILITY"},
						map[string]any{"text": " with "},
						map[string]any{"type": "file", "downloadCode": "FILE_CAPABILITY", "fileName": "error.log"},
					}}
				}
			}), testConfig, testNow)
			if err != nil || len(message.Attachments) == 0 || message.Text == "" || message.SenderID != "colleague" {
				t.Fatalf("media parse failed: %+v %v", message, err)
			}
			first := message.Attachments[0]
			if first.DownloadCode == "" || first.Kind == "" || first.Name == "" {
				t.Fatalf("attachment lost metadata: %+v", first)
			}
			if kind == "richText" && (message.Text != "@Helper inspect [图片 1] with [error.log]" || len(message.Attachments) != 2) {
				t.Fatalf("part order lost: %+v", message)
			}
			encoded, _ := json.Marshal(message)
			for _, output := range []string{string(encoded), fmt.Sprint(first), fmt.Sprintf("%+v", first), fmt.Sprintf("%#v", first)} {
				if strings.Contains(output, "CAPABILITY") {
					t.Fatalf("media capability leaked: %s", output)
				}
			}
		})
	}
}

func TestPrivatePicturesAcceptDistinctDownloadCodes(t *testing.T) {
	for _, kind := range []string{"picture", "richText"} {
		for _, tc := range []struct {
			name  string
			codes map[string]any
			want  string
		}{
			{"both provider codes", map[string]any{"downloadCode": "DOWNLOAD_CAPABILITY", "pictureDownloadCode": "PICTURE_CAPABILITY"}, "DOWNLOAD_CAPABILITY"},
			{"download code only", map[string]any{"downloadCode": "DOWNLOAD_CAPABILITY"}, "DOWNLOAD_CAPABILITY"},
			{"legacy picture code", map[string]any{"pictureDownloadCode": "PICTURE_CAPABILITY"}, "PICTURE_CAPABILITY"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				message, err := Parse(callbackPayload(t, func(m map[string]any) {
					m["msgtype"] = kind
					delete(m, "text")
					if kind == "richText" {
						tc.codes["type"] = "picture"
						m["content"] = map[string]any{"richText": []any{map[string]string{"text": "Before "}, tc.codes, map[string]string{"text": " after"}}}
					} else {
						m["content"] = tc.codes
					}
				}), testConfig, testNow)
				if err != nil || len(message.Attachments) != 1 || message.Attachments[0].DownloadCode != tc.want {
					t.Fatalf("documented private picture callback rejected: %+v %v", message, err)
				}
				wantText := "[图片 1]"
				if kind == "richText" {
					wantText = "Before [图片 1] after"
				}
				if message.Text != wantText {
					t.Fatalf("picture/text order lost: %q", message.Text)
				}
				encoded, _ := json.Marshal(message)
				if strings.Contains(string(encoded), "CAPABILITY") {
					t.Fatal("download code entered public message")
				}
			})
		}
	}
}

func TestMediaParsingCannotConvertInvalidRichTextIntoCommand(t *testing.T) {
	for _, content := range []any{
		map[string]any{"type": "picture", "downloadCode": ""},
		map[string]any{"type": "picture", "downloadCode": "one", "pictureDownloadCode": "bad\ncode"},
		map[string]any{"type": "picture", "downloadCode": "one", "url": "https://attacker.example/secret"},
		map[string]any{"type": "file", "downloadCode": "one", "fileName": "../../escape"},
		map[string]any{"type": "file", "downloadCode": "one", "fileName": "bad\nname"},
		map[string]any{"type": "file", "downloadCode": "one", "fileName": "file", "fileSize": MaxFileBytes + 1},
		map[string]any{"type": "file", "downloadCode": "one", "fileName": "file", "fileSize": -1},
		json.RawMessage(`{"type":"picture","downloadCode":"one","downloadCode":"two"}`),
	} {
		message, err := Parse(callbackPayload(t, func(m map[string]any) {
			m["msgtype"] = "richText"
			m["content"] = map[string]any{"richText": []any{map[string]string{"text": "/new"}, content}}
		}), testConfig, testNow)
		if err != nil || message.Text != "" || len(message.Attachments) != 0 || message.ID == "" {
			t.Fatalf("invalid media partially became a command: %+v %v", message, err)
		}
	}
	parts := []any{map[string]string{"text": "/new"}}
	for i := 0; i <= MaxAttachments; i++ {
		parts = append(parts, map[string]string{"type": "picture", "downloadCode": "code"})
	}
	message, err := Parse(callbackPayload(t, func(m map[string]any) {
		m["msgtype"] = "richText"
		m["content"] = map[string]any{"richText": parts}
	}), testConfig, testNow)
	if err != nil || message.Text != "" || len(message.Attachments) != 0 {
		t.Fatalf("too many attachments accepted: %+v %v", message, err)
	}
}

func TestMediaNeverWeakensVerifiedMentionOrEnterpriseBoundary(t *testing.T) {
	for _, modify := range []func(map[string]any){
		func(m map[string]any) { m["isInAtList"] = false },
		func(m map[string]any) { m["senderCorpId"] = "external" },
		func(m map[string]any) { m["atUsers"] = []any{map[string]string{"dingtalkId": "other-robot"}} },
	} {
		_, err := Parse(callbackPayload(t, func(m map[string]any) {
			m["conversationType"], m["isInAtList"] = "2", true
			m["msgtype"] = "picture"
			m["content"] = map[string]string{"downloadCode": "secret"}
			modify(m)
		}), testConfig, testNow)
		if err == nil {
			t.Fatal("unauthorized media accepted")
		}
	}
}
