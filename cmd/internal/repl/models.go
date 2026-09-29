package repl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type ModelRef struct {
	Provider string
	ID       string
}

type modelSelection struct {
	ref    ModelRef
	client agentrunner.Client
	effort llm.ReasoningEffort
	origin string
}

// ModelRouter owns provider clients. Respond snapshots its choice at call time,
// so a selection change affects the next request without replacing a live one.
type ModelRouter struct {
	updateMu   sync.Mutex
	mu         sync.Mutex
	requests   sync.WaitGroup
	config     *ConfigStore
	getenv     func(string) string
	providers  map[string]agentrunner.Provider
	selected   modelSelection
	clients    []agentrunner.Client
	active     map[uint64]ModelRef
	nextCall   uint64
	closed     bool
	provenance *responseProvenance
}

var _ llm.Adapter = (*ModelRouter)(nil)

func NewModelRouter(config *ConfigStore, getenv func(string) string) (*ModelRouter, error) {
	return newModelRouter(config, getenv, agentrunner.DefaultProviders())
}

func newModelRouter(config *ConfigStore, getenv func(string) string, providers []agentrunner.Provider) (*ModelRouter, error) {
	router := emptyModelRouter(config, getenv, providers)
	settings := config.Current()
	selection, err := router.prepare(settings.Model.Provider, settings.Model.ID, settings)
	if err != nil {
		return nil, err
	}
	router.selected = selection
	router.clients = append(router.clients, selection.client)
	return router, nil
}

// NewModelRouterSelection prepares a client before saving a selection when
// startup could not create the configured provider client.
func NewModelRouterSelection(config *ConfigStore, getenv func(string) string, provider, id string) (*ModelRouter, error) {
	router := emptyModelRouter(config, getenv, agentrunner.DefaultProviders())
	settings := config.Current()
	settings.Model.Provider, settings.Model.ID = provider, id
	selection, err := router.prepare(provider, id, settings)
	if err != nil {
		return nil, err
	}
	if err := config.SaveSettings(map[string]any{"model.provider": provider, "model.id": id, "model.context_window_tokens": 0}); err != nil {
		_ = selection.client.Close()
		return nil, err
	}
	router.selected = selection
	router.clients = append(router.clients, selection.client)
	return router, nil
}

func NewModelRouterSettings(config *ConfigStore, getenv func(string) string, changes map[string]any, candidate Config) (*ModelRouter, error) {
	router := emptyModelRouter(config, getenv, agentrunner.DefaultProviders())
	selection, err := router.prepare(candidate.Model.Provider, candidate.Model.ID, candidate)
	if err != nil {
		return nil, err
	}
	if err := config.SaveSettings(changes); err != nil {
		_ = selection.client.Close()
		return nil, err
	}
	router.selected = selection
	router.clients = append(router.clients, selection.client)
	return router, nil
}

func emptyModelRouter(config *ConfigStore, getenv func(string) string, providers []agentrunner.Provider) *ModelRouter {
	router := &ModelRouter{config: config, getenv: getenv, providers: make(map[string]agentrunner.Provider, len(providers)), active: make(map[uint64]ModelRef)}
	for _, provider := range providers {
		router.providers[provider.Name] = provider
	}
	return router
}

func (router *ModelRouter) prepare(providerName, modelID string, settings Config) (modelSelection, error) {
	provider, ok := router.providers[providerName]
	if !ok {
		return modelSelection{}, fmt.Errorf("unknown provider %q", providerName)
	}
	if modelID == "" {
		modelID = provider.DefaultModel
	}
	if strings.TrimSpace(modelID) == "" {
		return modelSelection{}, fmt.Errorf("provider %q needs a model ID", providerName)
	}
	apiKey := router.getenv("UNREAL_HARNESS_LLM_API_KEY")
	if apiKey == "" && provider.APIKeyEnvironment != "" {
		apiKey = router.getenv(provider.APIKeyEnvironment)
	}
	if apiKey == "" && provider.APIKeyEnvironment != "" {
		return modelSelection{}, fmt.Errorf("set %s or UNREAL_HARNESS_LLM_API_KEY for %s", provider.APIKeyEnvironment, providerName)
	}
	baseURL := provider.BaseURL
	if override := settings.Providers[providerName].BaseURL; override != "" {
		baseURL = override
	}
	client, err := provider.NewClient(apiKey, baseURL, settings.Model.MaxAttempts, router.getenv)
	if err != nil {
		return modelSelection{}, fmt.Errorf("create %s client: %w", providerName, err)
	}
	return modelSelection{
		ref:    ModelRef{Provider: providerName, ID: modelID},
		client: client, effort: llm.ReasoningEffort(settings.Model.ReasoningEffort),
		origin: providerName + "|" + strings.TrimRight(baseURL, "/"),
	}, nil
}

func (router *ModelRouter) Selected() ModelRef {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.selected.ref
}

func (router *ModelRouter) ActiveSelections() []ModelRef {
	router.mu.Lock()
	defer router.mu.Unlock()
	ids := make([]uint64, 0, len(router.active))
	for id := range router.active {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	choices := make([]ModelRef, 0, len(ids))
	for _, id := range ids {
		choices = append(choices, router.active[id])
	}
	return choices
}

func (router *ModelRouter) Select(provider, modelID string) error {
	settings := router.config.Current()
	previous := settings.Model
	settings.Model.Provider, settings.Model.ID = provider, modelID
	changes := map[string]any{"model.provider": provider, "model.id": modelID}
	if provider != previous.Provider || modelID != previous.ID {
		settings.Model.ContextWindowTokens = 0
		changes["model.context_window_tokens"] = 0
	}
	return router.ApplySettings(changes, settings)
}

func (router *ModelRouter) ApplySettings(changes map[string]any, candidate Config) error {
	router.updateMu.Lock()
	defer router.updateMu.Unlock()
	router.mu.Lock()
	closed := router.closed
	router.mu.Unlock()
	if closed {
		return errors.New("model router is closed")
	}
	var selection modelSelection
	refreshClient := modelSettingsChanged(changes, candidate.Model.Provider)
	if refreshClient {
		var err error
		selection, err = router.prepare(candidate.Model.Provider, candidate.Model.ID, candidate)
		if err != nil {
			return err
		}
	}
	if err := router.config.SaveSettings(changes); err != nil {
		if selection.client != nil {
			_ = selection.client.Close()
		}
		return err
	}
	if refreshClient {
		router.mu.Lock()
		router.selected = selection
		router.clients = append(router.clients, selection.client)
		router.mu.Unlock()
	}
	return nil
}

func modelSettingsChanged(changes map[string]any, selectedProvider string) bool {
	for key := range changes {
		if (strings.HasPrefix(key, "model.") && key != "model.context_window_tokens") || key == "providers."+selectedProvider+".base_url" {
			return true
		}
	}
	return false
}

func (router *ModelRouter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return llm.Response{}, errors.New("model router is closed")
	}
	selection := router.selected
	provenance := router.provenance
	router.nextCall++
	callID := router.nextCall
	router.active[callID] = selection.ref
	router.requests.Add(1)
	router.mu.Unlock()
	defer func() {
		router.mu.Lock()
		delete(router.active, callID)
		router.mu.Unlock()
		router.requests.Done()
	}()
	request.Model.ID = selection.ref.ID
	request.Model.ReasoningEffort = selection.effort
	if provenance != nil {
		request = provenance.project(request, selection.origin)
	}
	response, err := selection.client.Respond(ctx, request, options)
	if err == nil && provenance != nil {
		provenance.record(response, selection.origin)
	}
	return response, err
}

func (router *ModelRouter) SetProvenance(provenance *responseProvenance) {
	router.mu.Lock()
	defer router.mu.Unlock()
	router.provenance = provenance
}

func (router *ModelRouter) TakeProvenanceWarning() error {
	router.mu.Lock()
	provenance := router.provenance
	router.mu.Unlock()
	if provenance == nil {
		return nil
	}
	return provenance.takeWarning()
}

func (router *ModelRouter) Close() error {
	router.updateMu.Lock()
	defer router.updateMu.Unlock()
	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return nil
	}
	router.closed = true
	clients := append([]agentrunner.Client(nil), router.clients...)
	router.mu.Unlock()
	router.requests.Wait()
	var result error
	for _, client := range clients {
		result = errors.Join(result, client.Close())
	}
	return result
}

// Reload installs validated disk settings without writing the config file.
// Existing requests own their client snapshots until completion.
func (router *ModelRouter) Reload(candidate Config) error {
	router.updateMu.Lock()
	defer router.updateMu.Unlock()
	router.mu.Lock()
	closed := router.closed
	router.mu.Unlock()
	if closed {
		return errors.New("model router is closed")
	}
	selection, err := router.prepare(candidate.Model.Provider, candidate.Model.ID, candidate)
	if err != nil {
		return err
	}
	router.config.mu.Lock()
	router.config.active = cloneConfig(candidate)
	router.config.mu.Unlock()
	router.mu.Lock()
	router.selected = selection
	router.clients = append(router.clients, selection.client)
	router.mu.Unlock()
	return nil
}
