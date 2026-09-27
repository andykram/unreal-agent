package openaicodex

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestModelsRequestContract(t *testing.T) {
	for _, base := range []string{"", " " + BaseURL + "/ ", "http://127.0.0.1:1234/codex/"} {
		t.Run(base, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request, err := (Config{BaseURL: base, AccessToken: "access-token", AccountID: "account"}).ModelsRequest(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := BaseURL + "/models?client_version=0.155.0"
			if base == "http://127.0.0.1:1234/codex/" {
				want = "http://127.0.0.1:1234/codex/models?client_version=0.155.0"
			}
			if request.URL.String() != want || request.Method != http.MethodGet || request.Body != nil || request.Context() != ctx {
				t.Fatalf("unexpected catalog request: %s %s, body=%v", request.Method, request.URL, request.Body)
			}
			for key, want := range map[string]string{"Authorization": "Bearer access-token", "ChatGPT-Account-ID": "account", "originator": "unreal-agent", "User-Agent": "unreal-agent"} {
				if request.Header.Get(key) != want {
					t.Errorf("incorrect %s header", key)
				}
			}
		})
	}
}

func TestModelsRequestValidation(t *testing.T) {
	for name, config := range map[string]Config{
		"untrusted endpoint":   {BaseURL: "https://evil.example", AccessToken: "opaque", AccountID: "account"},
		"plaintext production": {BaseURL: "http://chatgpt.com/backend-api/codex", AccessToken: "opaque", AccountID: "account"},
		"missing token":        {AccountID: "account"},
		"missing account":      {AccessToken: "opaque"},
		"API key":              {AccessToken: "sk-api-key", AccountID: "account"},
		"invalid header":       {AccessToken: "opaque\r\nInjected: header", AccountID: "account"},
		"conflicting sources":  {AccessToken: "opaque", AccountID: "account", AuthFile: "unused"},
		"missing auth file":    {AuthFile: filepath.Join(t.TempDir(), "missing.json")},
	} {
		t.Run(name, func(t *testing.T) {
			if request, err := config.ModelsRequest(t.Context()); err == nil || request != nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestModelsRequestAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTestAuth(t, path, "file-token", "file-account")
	config := Config{AuthFile: path}
	request, err := config.ModelsRequest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer file-token" || request.Header.Get("ChatGPT-Account-ID") != "file-account" {
		t.Fatal("auth file credentials not used")
	}
	if err := os.WriteFile(path, []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if request, err := config.ModelsRequest(t.Context()); err == nil || request != nil {
		t.Fatal("invalid auth file accepted")
	}
}
