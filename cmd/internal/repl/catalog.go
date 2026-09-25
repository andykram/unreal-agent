package repl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
)

type ModelChoice struct {
	Ref                 ModelRef
	Label               string
	Source              string
	Availability        string
	ContextWindowTokens int
}

type CatalogResult struct {
	Provider   string
	Generation uint64
	Choices    []ModelChoice
	Err        error
	cachePath  string
}

type ModelCatalog struct {
	mu         sync.Mutex
	config     *ConfigStore
	getenv     func(string) string
	client     *http.Client
	providers  map[string]agentrunner.Provider
	latest     map[string]uint64
	discovered map[string][]ModelChoice
}

func NewModelCatalog(config *ConfigStore, getenv func(string) string) *ModelCatalog {
	catalog := &ModelCatalog{
		config: config, getenv: getenv,
		client:    &http.Client{Timeout: 3 * time.Second},
		providers: make(map[string]agentrunner.Provider), latest: make(map[string]uint64),
		discovered: make(map[string][]ModelChoice),
	}
	for _, provider := range agentrunner.DefaultProviders() {
		catalog.providers[provider.Name] = provider
	}
	return catalog
}

func (catalog *ModelCatalog) ProviderNames() []string {
	names := make([]string, 0, len(catalog.providers))
	for name := range catalog.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (catalog *ModelCatalog) ConfiguredProviders() []string {
	settings := catalog.config.Current()
	var result []string
	for name, provider := range catalog.providers {
		if name == settings.Model.Provider {
			result = append(result, name)
			continue
		}
		if _, configured := settings.Providers[name]; configured {
			result = append(result, name)
			continue
		}
		if provider.APIKeyEnvironment != "" && (catalog.getenv(provider.APIKeyEnvironment) != "" || catalog.getenv("UNREAL_HARNESS_LLM_API_KEY") != "") {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func (catalog *ModelCatalog) Choices() []ModelChoice {
	settings := catalog.config.Current()
	merged := make(map[ModelRef]ModelChoice)
	for name, provider := range catalog.providers {
		if provider.DefaultModel != "" {
			choice := catalogChoice(name, provider.DefaultModel, "default", "unknown")
			merged[choice.Ref] = choice
		}
	}
	for name, config := range settings.Providers {
		for _, id := range config.Models {
			choice := catalogChoice(name, id, "configured", "unknown")
			merged[choice.Ref] = choice
		}
	}
	selected := ModelRef{Provider: settings.Model.Provider, ID: settings.Model.ID}
	if selected.ID != "" {
		if _, exists := merged[selected]; !exists {
			merged[selected] = catalogChoice(selected.Provider, selected.ID, "manual", "unknown")
		}
	}
	catalog.mu.Lock()
	for _, choices := range catalog.discovered {
		for _, choice := range choices {
			if existing, found := merged[choice.Ref]; found {
				existing.Availability = choice.Availability
				existing.ContextWindowTokens = choice.ContextWindowTokens
				merged[choice.Ref] = existing
			} else {
				merged[choice.Ref] = choice
			}
		}
	}
	catalog.mu.Unlock()
	result := make([]ModelChoice, 0, len(merged))
	for _, choice := range merged {
		result = append(result, choice)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ref.Provider != result[j].Ref.Provider {
			return result[i].Ref.Provider < result[j].Ref.Provider
		}
		return result[i].Ref.ID < result[j].Ref.ID
	})
	return result
}

func (catalog *ModelCatalog) ContextWindow(ref ModelRef) int {
	if catalog == nil {
		return 0
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	for _, choice := range catalog.discovered[ref.Provider] {
		if choice.Ref == ref {
			return choice.ContextWindowTokens
		}
	}
	return 0
}

func catalogChoice(provider, id, source, availability string) ModelChoice {
	return ModelChoice{Ref: ModelRef{Provider: provider, ID: id}, Label: provider + " · " + id, Source: source, Availability: availability}
}

func (catalog *ModelCatalog) BeginRefresh(provider string) uint64 {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.latest[provider]++
	return catalog.latest[provider]
}

func (catalog *ModelCatalog) Discover(ctx context.Context, provider string, generation uint64) CatalogResult {
	return catalog.discover(ctx, provider, generation, false)
}

func (catalog *ModelCatalog) Refresh(ctx context.Context, provider string, generation uint64) CatalogResult {
	return catalog.discover(ctx, provider, generation, true)
}

func (catalog *ModelCatalog) discover(ctx context.Context, provider string, generation uint64, force bool) CatalogResult {
	result := CatalogResult{Provider: provider, Generation: generation}
	definition, supported := catalog.providers[provider]
	if !supported {
		result.Err = fmt.Errorf("unknown provider %q", provider)
		return result
	}
	base := definition.BaseURL
	if override := catalog.config.Current().Providers[provider].BaseURL; override != "" {
		base = override
	}
	var endpoint string
	switch provider {
	case "openai-codex":
		return catalog.discoverCodex(ctx, base, result, force)
	case "openai", "openrouter":
		endpoint = strings.TrimSuffix(base, "/") + "/models"
	case "ollama", "ollama-cloud":
		parsed, err := url.Parse(base)
		if err != nil || parsed.Host == "" || !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/v1") {
			result.Err = fmt.Errorf("ollama base URL %q must end in /v1 to derive /api/tags", base)
			return result
		}
		parsed.Path = strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), "/v1") + "/api/tags"
		parsed.RawQuery = ""
		endpoint = parsed.String()
	default:
		result.Err = fmt.Errorf("%s has no verified inference model catalog; use configured or manual IDs", provider)
		return result
	}
	credential := ""
	if provider != "ollama" {
		credential = catalog.getenv("UNREAL_HARNESS_LLM_API_KEY")
		if credential == "" {
			credential = catalog.getenv(definition.APIKeyEnvironment)
		}
		if credential == "" {
			result.Err = fmt.Errorf("set %s for %s model discovery", definition.APIKeyEnvironment, provider)
			return result
		}
		if path, err := catalogCachePath(catalog.getenv, provider, endpoint, credential); err == nil {
			result.cachePath = path
			if !force {
				if choices, err := readCatalogCache(path, provider); err == nil && (provider != "ollama-cloud" || cacheYoungerThan(path, 24*time.Hour)) {
					result.Choices = choices
					result.cachePath = ""
					return result
				}
			}
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		result.Err = err
		return result
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := catalog.client.Do(request)
	if err != nil {
		result.Err = fmt.Errorf("discover %s models: %w", provider, err)
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Err = fmt.Errorf("discover %s models: HTTP %d", provider, response.StatusCode)
		return result
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		result.Err = err
		return result
	}
	if len(encoded) > 2<<20 {
		result.Err = errors.New("model catalog response exceeds 2 MiB")
		return result
	}
	var choices []ModelChoice
	if provider == "ollama" || provider == "ollama-cloud" {
		var parsed struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			result.Err = fmt.Errorf("parse ollama catalog: %w", err)
			return result
		}
		for _, model := range parsed.Models {
			id := model.Model
			if id == "" {
				id = model.Name
			}
			choices = append(choices, catalogChoice(provider, id, "discovered", "available"))
		}
		// Cloud models serve at their manifest context length, so the per-model
		// show endpoint is authoritative. Local models serve at a runtime
		// num_ctx that the manifest cannot report.
		if provider == "ollama-cloud" {
			catalog.ollamaCloudContextLengths(ctx, base, credential, choices)
		}
	} else {
		var parsed struct {
			Data []struct {
				ID            string `json:"id"`
				ContextLength int    `json:"context_length"`
			} `json:"data"`
		}
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			result.Err = fmt.Errorf("parse %s catalog: %w", provider, err)
			return result
		}
		for _, model := range parsed.Data {
			choice := catalogChoice(provider, model.ID, "discovered", "available")
			if model.ContextLength > 0 {
				choice.ContextWindowTokens = model.ContextLength
			}
			choices = append(choices, choice)
		}
	}
	seen := map[string]struct{}{}
	for _, choice := range choices {
		id := choice.Ref.ID
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result.Choices = append(result.Choices, choice)
	}
	return result
}

// ollamaCloudContextLengths fills choices with the context length that
// ollama.com reports per cloud model. A model whose show request fails keeps
// zero, so discovery still returns the model list it did fetch.
func (catalog *ModelCatalog) ollamaCloudContextLengths(ctx context.Context, base, credential string, choices []ModelChoice) {
	if len(choices) == 0 {
		return
	}
	endpoint := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1") + "/api/show"
	limits := make([]int, len(choices))
	queue := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(choices)) {
		workers.Go(func() {
			for index := range queue {
				limits[index] = catalog.ollamaShowContextLength(ctx, endpoint, credential, choices[index].Ref.ID)
			}
		})
	}
	for index := range choices {
		queue <- index
	}
	close(queue)
	workers.Wait()
	for index, limit := range limits {
		if limit > 0 {
			choices[index].ContextWindowTokens = limit
		}
	}
}

func (catalog *ModelCatalog) ollamaShowContextLength(ctx context.Context, endpoint, credential, model string) int {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := catalog.client.Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(encoded) > 1<<20 {
		return 0
	}
	var parsed struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		return 0
	}
	for key, value := range parsed.ModelInfo {
		if !strings.HasSuffix(key, ".context_length") {
			continue
		}
		if number, ok := value.(float64); ok && number > 0 {
			return int(number)
		}
	}
	return 0
}

func (catalog *ModelCatalog) Apply(result CatalogResult) bool {
	if result.Err != nil {
		return false
	}
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if result.Generation != catalog.latest[result.Provider] {
		return false
	}
	catalog.discovered[result.Provider] = append([]ModelChoice(nil), result.Choices...)
	if result.cachePath != "" {
		_ = writeCatalogCache(result.cachePath, result.Choices)
	}
	return true
}
