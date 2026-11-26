package mods

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jonasthim/valheim-server-ui/internal/domain"
)

type profileCodeTestTransport func(*http.Request) (*http.Response, error)

func (f profileCodeTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestThunderstoreProfileCodeRedirect(t *testing.T) {
	const key = "12345678-1234-1234-1234-123456789abc"
	const storagePath = "/live/modpacks/legacyprofile/" + key
	for _, tc := range []struct {
		name, target string
		wantCalls    int
	}{
		{name: "CDN", target: "https://ccdn.thunderstore.io" + storagePath, wantCalls: 2},
		{name: "other host", target: "https://example.invalid" + storagePath, wantCalls: 1},
		{name: "other resource", target: "https://ccdn.thunderstore.io" + storagePath + "0", wantCalls: 1},
		{name: "without TLS", target: "http://ccdn.thunderstore.io" + storagePath, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: profileCodeTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if deadline, ok := req.Context().Deadline(); !ok || deadline.IsZero() || req.Method != http.MethodGet {
					t.Errorf("unexpected profile request %v", req)
				}
				if calls == 1 {
					if req.URL.String() != thunderstoreProfileURL+key+"/" {
						t.Errorf("unexpected profile URL %s", req.URL)
					}
					return &http.Response{
						StatusCode: http.StatusFound,
						Header:     http.Header{"Location": {tc.target}},
						Body:       http.NoBody,
					}, nil
				}
				if req.URL.String() != tc.target {
					t.Errorf("unexpected CDN URL %s", req.URL)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("#r2modman\r\nZml4dHVyZQ==\n")),
				}, nil
			})}
			registry := &Thunderstore{http: client}
			data, err := registry.downloadProfileCode(context.Background(), key)
			if tc.wantCalls == 1 {
				if err == nil || domain.AsError(err).Code != domain.CodeUpstreamError {
					t.Fatalf("expected an upstream error, got %v", err)
				}
			} else if err != nil || string(data) != "fixture" {
				t.Fatalf("got %q, %v", data, err)
			}
			if calls != tc.wantCalls || client.CheckRedirect != nil {
				t.Fatalf("got %d requests, want %d, or modified the shared client", calls, tc.wantCalls)
			}
		})
	}
}

func TestDecodeProfileCodeInvalidData(t *testing.T) {
	for _, tc := range []struct{ name, data, message string }{
		{name: "marker", data: "#other\nZml4dHVyZQ==", message: "Thunderstore returned an unsupported profile format"},
		{name: "base64", data: "#r2modman\nxx??", message: "Thunderstore returned invalid profile data"},
		{name: "too much decoded data", data: "#r2modman\n" + base64.StdEncoding.EncodeToString(make([]byte, maxModProfileBytes+1)), message: "mod profile ZIP must be 8 MiB or smaller"},
		{name: "too much encoded data", data: "#r2modman\n" + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxModProfileBytes)+20), message: "mod profile ZIP must be 8 MiB or smaller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeProfileCode(strings.NewReader(tc.data))
			if err == nil {
				t.Fatal("expected a validation error")
			}
			de := domain.AsError(err)
			if de.Code != domain.CodeValidationFailed || len(de.Fields) != 1 || de.Fields[0] != (domain.FieldError{Field: "code", Message: tc.message}) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDecodeProfileCodeArchiveLimit(t *testing.T) {
	want := make([]byte, maxModProfileBytes)
	body := thunderstoreProfileMarker + "\r\n" + base64.StdEncoding.EncodeToString(want) + "\r\n"
	data, err := decodeProfileCode(strings.NewReader(body))
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("decode archive at limit: length=%d, err=%v", len(data), err)
	}
}
