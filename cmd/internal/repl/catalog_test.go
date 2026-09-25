package repl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCatalogMergesConfiguredAndDiscoveredProviderIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			output.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(output, `{"data":[{"id":"shared/model","context_length":64000},{"id":"extra/model"}]}`)
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" || key == "OPENROUTER_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(map[string]any{
		"providers.openai.base_url":     server.URL + "/v1",
		"providers.openai.models":       []string{"shared/model", "manual/id"},
		"providers.openrouter.base_url": server.URL + "/v1",
		"providers.openrouter.models":   []string{"shared/model"},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	for _, provider := range []string{"openai", "openrouter"} {
		generation := catalog.BeginRefresh(provider)
		result := catalog.Discover(t.Context(), provider, generation)
		if result.Err != nil || !catalog.Apply(result) {
			t.Fatalf("discover %s: %v", provider, result.Err)
		}
	}
	seen := map[ModelRef]ModelChoice{}
	for _, choice := range catalog.Choices() {
		seen[choice.Ref] = choice
	}
	for _, provider := range []string{"openai", "openrouter"} {
		ref := ModelRef{Provider: provider, ID: "shared/model"}
		if seen[ref].Availability != "available" {
			t.Fatalf("shared ID under %s = %#v", provider, seen[ref])
		}
		if got := catalog.ContextWindow(ref); got != 64000 {
			t.Fatalf("%s context window = %d", provider, got)
		}
	}
	if seen[ModelRef{Provider: "openai", ID: "manual/id"}].Source != "configured" {
		t.Fatal("configured manual ID disappeared after discovery")
	}
	if _, exists := seen[ModelRef{Provider: "ollama", ID: "shared/model"}]; exists {
		t.Fatal("cross-provider ID was invented")
	}
}

func TestSelectedOpenRouterContextWindowRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("path = %s", request.URL.Path)
		}
		fmt.Fprint(output, `{"data":[{"id":"other/family","context_length":48000}]}`)
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENROUTER_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(map[string]any{"model.provider": "openrouter", "model.id": "other/family", "providers.openrouter.base_url": server.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	state, err := OpenSessionState(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := newUI(t.Context(), getenv, 0, state, config, nil, NewModelCatalog(config, getenv), nil, nil)
	command := model.selectedContextCatalogCmd()
	if command == nil {
		t.Fatal("selected OpenRouter model was not refreshed")
	}
	model.Update(command())
	if got, source := model.contextCapacity(); got != 48000 || source != "provider catalog" {
		t.Fatalf("discovered context = %d, %q", got, source)
	}
}

func TestModelCatalogFailureKeepsLastSuccessfulEntries(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		code := int(status.Load())
		output.WriteHeader(code)
		if code == http.StatusOK {
			fmt.Fprint(output, `{"data":[{"id":"kept"}]}`)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.openai.base_url", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	good := catalog.Discover(t.Context(), "openai", catalog.BeginRefresh("openai"))
	if good.Err != nil || !catalog.Apply(good) {
		t.Fatalf("initial catalog = %v", good.Err)
	}
	status.Store(http.StatusTooManyRequests)
	failed := catalog.Refresh(t.Context(), "openai", catalog.BeginRefresh("openai"))
	if failed.Err == nil || !strings.Contains(failed.Err.Error(), "429") || catalog.Apply(failed) {
		t.Fatalf("failed refresh = %v", failed.Err)
	}
	if len(catalog.Choices()) < 2 {
		t.Fatal("failed refresh discarded successful or default entries")
	}
	newer := catalog.BeginRefresh("openai")
	if catalog.Apply(CatalogResult{Provider: "openai", Generation: newer - 1, Choices: []ModelChoice{catalogChoice("openai", "stale", "discovered", "available")}}) {
		t.Fatal("stale catalog result replaced newer refresh")
	}
}

func TestRemoteModelCatalogPersistsUntilExplicitRefresh(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected catalog request: %s %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		count := requests.Add(1)
		fmt.Fprintf(output, `{"data":[{"id":"model-%d","context_length":64000}]}`, count)
	}))
	defer server.Close()
	home := t.TempDir()
	cacheHome := filepath.Join(home, "cache")
	getenv := func(key string) string {
		switch key {
		case "OPENAI_API_KEY":
			return "test-key"
		case "XDG_CACHE_HOME":
			return cacheHome
		default:
			return testEnv(home, "")(key)
		}
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.openai.base_url", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	first := NewModelCatalog(config, getenv)
	initial := first.Discover(t.Context(), "openai", first.BeginRefresh("openai"))
	if initial.Err != nil || !first.Apply(initial) {
		t.Fatalf("initial discovery: %v", initial.Err)
	}
	path, err := catalogCachePath(getenv, "openai", server.URL+"/v1/models", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache file: %v, mode %v", err, info)
	}
	second := NewModelCatalog(config, getenv)
	cached := second.Discover(t.Context(), "openai", second.BeginRefresh("openai"))
	if cached.Err != nil || !second.Apply(cached) || cached.Choices[0].Source != "cached" || cached.Choices[0].ContextWindowTokens != 64000 {
		t.Fatalf("cached discovery: %#v", cached)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("ordinary discovery made %d requests", got)
	}
	refreshed := second.Refresh(t.Context(), "openai", second.BeginRefresh("openai"))
	if refreshed.Err != nil || !second.Apply(refreshed) || refreshed.Choices[0].Ref.ID != "model-2" {
		t.Fatalf("forced refresh: %#v", refreshed)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("forced refresh made %d requests", got)
	}
	third := NewModelCatalog(config, getenv)
	replaced := third.Discover(t.Context(), "openai", third.BeginRefresh("openai"))
	if replaced.Err != nil || !third.Apply(replaced) || replaced.Choices[0].Ref.ID != "model-2" || requests.Load() != 2 {
		t.Fatalf("replaced cache: %#v; requests %d", replaced, requests.Load())
	}
}

func TestRemoteModelCatalogCacheSeparatesCredentialsAndRepairsCorruption(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		fmt.Fprint(output, `{"data":[{"id":"ready"}]}`)
	}))
	defer server.Close()
	home := t.TempDir()
	credential := "first-key"
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return credential
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.openai.base_url", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	load := func() {
		t.Helper()
		result := catalog.Discover(t.Context(), "openai", catalog.BeginRefresh("openai"))
		if result.Err != nil || !catalog.Apply(result) {
			t.Fatalf("discovery: %v", result.Err)
		}
	}
	load()
	credential = "second-key"
	load()
	if got := requests.Load(); got != 2 {
		t.Fatalf("credential change made %d requests", got)
	}
	path, err := catalogCachePath(getenv, "openai", server.URL+"/v1/models", credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	load()
	if got := requests.Load(); got != 3 {
		t.Fatalf("corrupt cache repair made %d requests", got)
	}
	if choices, err := readCatalogCache(path, "openai"); err != nil || len(choices) != 1 {
		t.Fatalf("cache repair: %v, %#v", err, choices)
	}
}

func TestModelCatalogReportsMalformedAndTimedOutDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow/models" {
			<-request.Context().Done()
			return
		}
		fmt.Fprint(output, `{malformed`)
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.openai.base_url", server.URL+"/bad"); err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	if result := catalog.Discover(t.Context(), "openai", catalog.BeginRefresh("openai")); result.Err == nil {
		t.Fatal("malformed response succeeded")
	}
	if err := config.SaveSetting("providers.openai.base_url", server.URL+"/slow"); err != nil {
		t.Fatal(err)
	}
	catalog.client.Timeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if result := catalog.Discover(ctx, "openai", catalog.BeginRefresh("openai")); result.Err == nil {
		t.Fatal("timed-out discovery succeeded")
	}
}

func TestCodexCatalogCachesAccountMetadataAndRefusesRedirects(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/models" || r.URL.Query().Get("client_version") == "" || r.Header.Get("Authorization") != "Bearer subscription" || r.Header.Get("ChatGPT-Account-ID") != "account" {
			t.Errorf("unexpected catalog request")
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"models":[{"slug":"family/model","context_window":272000}]}`)
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		switch key {
		case "OPENAI_CODEX_ACCESS_TOKEN":
			return "subscription"
		case "OPENAI_CODEX_ACCOUNT_ID":
			return "account"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(map[string]any{"providers.openai-codex.base_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		catalog := NewModelCatalog(config, getenv)
		result := catalog.Discover(t.Context(), "openai-codex", catalog.BeginRefresh("openai-codex"))
		if result.Err != nil || !catalog.Apply(result) {
			t.Fatalf("catalog: %v", result.Err)
		}
		if got := catalog.ContextWindow(ModelRef{Provider: "openai-codex", ID: "family/model"}); got != 272000 {
			t.Fatalf("limit = %d", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache missed: %d calls", calls.Load())
	}
	catalog := NewModelCatalog(config, getenv)
	result := catalog.Refresh(t.Context(), "openai-codex", catalog.BeginRefresh("openai-codex"))
	if result.Err != nil || calls.Load() != 2 {
		t.Fatalf("explicit refresh: %v, %d calls", result.Err, calls.Load())
	}
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed subscription redirect") }))
	defer redirected.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, redirected.URL, http.StatusFound) }))
	defer redirect.Close()
	if err := config.SaveSettings(map[string]any{"providers.openai-codex.base_url": redirect.URL}); err != nil {
		t.Fatal(err)
	}
	result = catalog.Refresh(t.Context(), "openai-codex", catalog.BeginRefresh("openai-codex"))
	if result.Err == nil || !strings.Contains(result.Err.Error(), "HTTP 302") {
		t.Fatalf("redirect: %v", result.Err)
	}
}

func TestOllamaCloudCatalogCachesForOneDay(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cloud-key" {
			t.Errorf("request = %s, auth = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"cloud-%d"}]}`, requests.Add(1))
		case "/api/show":
			if r.Method != http.MethodPost {
				t.Errorf("show method = %s", r.Method)
			}
			fmt.Fprintf(w, `{"model_info":{"cloud.context_length":4096}}`)
		default:
			t.Errorf("unexpected request = %s", r.URL.Path)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OLLAMA_API_KEY" {
			return "cloud-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.ollama-cloud.base_url", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	load := func(force bool) CatalogResult {
		t.Helper()
		catalog := NewModelCatalog(config, getenv)
		generation := catalog.BeginRefresh("ollama-cloud")
		var result CatalogResult
		if force {
			result = catalog.Refresh(t.Context(), "ollama-cloud", generation)
		} else {
			result = catalog.Discover(t.Context(), "ollama-cloud", generation)
		}
		if result.Err != nil || !catalog.Apply(result) {
			t.Fatalf("catalog: %+v", result)
		}
		return result
	}
	if got := load(false); got.Choices[0].Ref.ID != "cloud-1" || got.Choices[0].ContextWindowTokens != 4096 {
		t.Fatal(got)
	}
	path, err := catalogCachePath(getenv, "ollama-cloud", server.URL+"/api/tags", "cloud-key")
	if err != nil {
		t.Fatal(err)
	}
	if got := load(false); got.Choices[0].Source != "cached" || requests.Load() != 1 {
		t.Fatalf("cache miss: %+v, %d", got, requests.Load())
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := load(false); got.Choices[0].Ref.ID != "cloud-2" || requests.Load() != 2 {
		t.Fatalf("stale cache: %+v, %d", got, requests.Load())
	}
	if got := load(true); got.Choices[0].Ref.ID != "cloud-3" || requests.Load() != 3 {
		t.Fatalf("refresh: %+v, %d", got, requests.Load())
	}
}

func TestOllamaCloudCatalogFetchesPerModelContextWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"alpha"},{"name":"beta"}]}`)
		case "/api/show":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"model":"alpha"}` && string(body) != `{"model":"beta"}` {
				t.Errorf("show body = %q", body)
			}
			if string(body) == `{"model":"beta"}` {
				http.Error(w, "missing", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"model_info":{"qwen3.context_length":262144,"general.architecture":"qwen3"}}`)
		default:
			t.Errorf("unexpected request = %s", r.URL.Path)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	getenv := func(key string) string {
		if key == "OLLAMA_API_KEY" {
			return "cloud-key"
		}
		return testEnv(home, "")(key)
	}
	config, err := LoadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSetting("providers.ollama-cloud.base_url", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	catalog := NewModelCatalog(config, getenv)
	result := catalog.Discover(t.Context(), "ollama-cloud", catalog.BeginRefresh("ollama-cloud"))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	limits := map[string]int{}
	for _, choice := range result.Choices {
		limits[choice.Ref.ID] = choice.ContextWindowTokens
	}
	if limits["alpha"] != 262144 || limits["beta"] != 0 {
		t.Fatalf("limits = %v", limits)
	}
	if !catalog.Apply(result) {
		t.Fatal("apply failed")
	}
	if got := catalog.ContextWindow(ModelRef{Provider: "ollama-cloud", ID: "alpha"}); got != 262144 {
		t.Fatalf("context window = %d", got)
	}
}
