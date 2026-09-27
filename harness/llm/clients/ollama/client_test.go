package ollama

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
)

func TestClientUsesResponsesWithoutAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("incorrect request headers")
		}
		var body map[string]any
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		if body["model"] != "local-model:tag" || body["stream"] != true || body["store"] != false || body["prompt_cache_key"] != nil {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Respond(t.Context(), llm.Request{Model: llm.Model{ID: "local-model:tag"}}, llm.RequestOptions{CacheKey: "session"})
	if err != nil || response.ID != "r" || response.Stop != llm.StopComplete {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
}

func TestCloudClientAuthenticatesResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer cloud-key" {
			t.Errorf("request = %s, auth = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/v1", APIKey: "cloud-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Respond(t.Context(), llm.Request{Model: llm.Model{ID: "cloud-model"}}, llm.RequestOptions{})
	if err != nil || response.ID != "r" {
		t.Fatalf("response = %+v, error = %v", response, err)
	}
}

func TestAuthenticatedEndpointPolicy(t *testing.T) {
	for _, endpoint := range []string{"", "https://ollama.com/v1", "http://localhost:11434/v1", "http://127.0.0.1:11434/v1", "http://[::1]:11434/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := NewClient(Config{BaseURL: endpoint, APIKey: "key"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
		})
	}
	for _, endpoint := range []string{"http://ollama.com/v1", "http://192.168.1.2:11434/v1", "http://localhost.example/v1", "http://[::2]/v1", "https:///v1", "ftp://localhost/v1", "https://user:pass@ollama.com/v1", "https://ollama.com/v1?x=y", "https://ollama.com/v1#fragment", "://invalid"} {
		t.Run(endpoint, func(t *testing.T) {
			if client, err := NewClient(Config{BaseURL: endpoint, APIKey: "key"}); err == nil || client != nil {
				t.Fatal("unsafe authenticated endpoint accepted")
			}
		})
	}
	// Unauthenticated LAN installations remain supported.
	client, err := NewClient(Config{BaseURL: "http://192.168.1.2:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
}

func TestAuthenticatedClientRejectsRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Respond(t.Context(), llm.Request{Model: llm.Model{ID: "model"}}, llm.RequestOptions{})
	var apiErr *responsesapi.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("redirect error=%v, target requests=%d", err, redirected.Load())
	}
}
