// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

const testToken = "123456789:TEST_ONLY_NOT_A_REAL_TOKEN_123456789"

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestClientPollBatchFitsLegalEscapedLongMessages(t *testing.T) {
	message := `{"message_id":1,"date":1,"chat":{"id":-1,"type":"supergroup"},"text":"` + strings.Repeat(`\u4e2d`, 4096) + `","reply_to_message":{"text":"` + strings.Repeat(`\u4e2d`, 4096) + `"},"entities":[` + strings.Repeat(`{"type":"bold","offset":0,"length":1},`, 99) + `{"type":"bold","offset":0,"length":1}]}`
	updates := make([]string, 20)
	for i := range updates {
		updates[i] = fmt.Sprintf(`{"update_id":%d,"message":%s}`, i+1, message)
	}
	body := `{"ok":true,"result":[` + strings.Join(updates, ",") + `]}`
	if len(body) <= 1<<20 {
		t.Fatal("fixture does not reproduce the response bound")
	}
	client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Limit int `json:"limit"`
		}
		if common.DecodeJson(r.Body, &payload) != nil || payload.Limit != 20 {
			t.Errorf("poll batch is not bounded to 20: %d", payload.Limit)
		}
		return testResponse(200, body), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.GetUpdates(context.Background(), 0)
	if err != nil || len(got) != 20 {
		t.Fatalf("legal long-message batch rejected: count=%d err=%v", len(got), err)
	}
}

func TestClientFixedHostAndSanitizedFailures(t *testing.T) {
	client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot"+testToken+"/getUpdates" {
			t.Errorf("unsafe Bot API URL: host=%s", r.URL.Host)
		}
		return nil, errors.New("transport failure " + r.URL.String())
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetUpdates(context.Background(), 0)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(fmt.Sprintf("%+v %#v %+v %#v", client, client, *client, *client), testToken) {
		t.Fatal("token escaped a failure or client formatting")
	}
}

func TestClientRejectsRedirectAndHonorsRetryAfter(t *testing.T) {
	for _, status := range []int{302, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				response := testResponse(status, `{"ok":false,"description":"private token `+testToken+`","parameters":{"retry_after":17}}`)
				response.Header.Set("Location", "https://example.invalid/steal")
				return response, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetUpdates(context.Background(), 0)
			if err == nil || calls != 1 || strings.Contains(err.Error(), testToken) {
				t.Fatalf("redirect/retry unsafe: calls=%d err=%v", calls, err)
			}
			if status == 429 {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.RetryAfter.Seconds() != 17 {
					t.Fatalf("lost retry_after: %v", err)
				}
			}
		})
	}
}

func TestClientVerifyIsReadOnlyAndRejectsExistingWebhook(t *testing.T) {
	for _, tc := range []struct {
		name, me, chat, webhook string
		ok                      bool
	}{
		{"valid", `{"id":123456789,"is_bot":true,"username":"ztapi_bot"}`, `{"id":42,"type":"private"}`, `{"url":""}`, true},
		{"wrong_bot", `{"id":999,"is_bot":true,"username":"ztapi_bot"}`, `{"id":42,"type":"private"}`, `{"url":""}`, false},
		{"not_private", `{"id":123456789,"is_bot":true,"username":"ztapi_bot"}`, `{"id":42,"type":"group"}`, `{"url":""}`, false},
		{"webhook", `{"id":123456789,"is_bot":true,"username":"ztapi_bot"}`, `{"id":42,"type":"private"}`, `{"url":"https://example.invalid/private"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				result := ""
				switch method {
				case "getMe":
					result = tc.me
				case "getChat":
					result = tc.chat
				case "getWebhookInfo":
					result = tc.webhook
				default:
					t.Errorf("verification mutated Telegram via %s", method)
				}
				return testResponse(200, `{"ok":true,"result":`+result+`}`), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			err = client.Verify(context.Background(), 42, 123456789, "ztapi_bot")
			if (err == nil) != tc.ok {
				t.Fatalf("verify result: %v", err)
			}
		})
	}
}

func testImage(t *testing.T, kind string) []byte {
	t.Helper()
	if kind == "webp" {
		// Lossless fixture from golang.org/x/image testdata (BSD-3-Clause).
		data, err := base64.StdEncoding.DecodeString("UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==")
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var body bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 200, A: 255})
	var err error
	if kind == "jpeg" {
		err = jpeg.Encode(&body, picture, nil)
	} else {
		err = png.Encode(&body, picture)
	}
	if err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestClientDownloadsOnlyValidatedCurrentImage(t *testing.T) {
	for _, kind := range []string{"png", "jpeg", "webp"} {
		t.Run(kind, func(t *testing.T) {
			data := testImage(t, kind)
			calls := 0
			client, err := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "api.telegram.org" || r.URL.Scheme != "https" {
					t.Fatal("download escaped fixed origin")
				}
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					return testResponse(200, `{"ok":true,"result":{"file_path":"photos/current.`+kind+`","file_size":`+fmt.Sprint(len(data))+`}}`), nil
				}
				if r.URL.Path != "/file/bot"+testToken+"/photos/current."+kind {
					t.Fatal("unexpected file path")
				}
				return testResponse(200, string(data)), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.DownloadImage(context.Background(), "current_file_id")
			if err != nil || got == nil || got.MIMEType != "image/"+kind || got.DataBase64 != base64.StdEncoding.EncodeToString(data) || calls != 2 {
				t.Fatalf("invalid image: %+v %v calls=%d", got, err, calls)
			}
		})
	}
}

func TestClientRejectsUnsafePathsOversizeAndInvalidImages(t *testing.T) {
	for _, path := range []string{"../secret.png", "photos/../secret.png", "photos//current.png", "https://example.invalid/x.png", "photos/a.png?x=y", "photos/%2e%2e/x.png", "photos/a\\b.png"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			client, _ := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return testResponse(200, `{"ok":true,"result":{"file_path":"`+strings.ReplaceAll(path, "\\", "\\\\")+`"}}`), nil
			})})
			if _, err := client.DownloadImage(context.Background(), "file"); err == nil || calls != 1 {
				t.Fatalf("unsafe path accepted: %v calls=%d", err, calls)
			}
		})
	}
	for _, data := range []string{"not an image", strings.Repeat("x", support.MaxImageBytes+1)} {
		client, _ := NewClient(testToken, &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "getFile") {
				return testResponse(200, `{"ok":true,"result":{"file_path":"photos/file.png"}}`), nil
			}
			return testResponse(200, data), nil
		})})
		if _, err := client.DownloadImage(context.Background(), "file"); err == nil {
			t.Fatal("invalid or oversized image accepted")
		}
	}
}
