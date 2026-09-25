package repl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openaicodex"
)

func (catalog *ModelCatalog) discoverCodex(ctx context.Context, base string, result CatalogResult, force bool) CatalogResult {
	config, err := openaicodex.EnvironmentConfig(catalog.getenv)
	if err != nil {
		result.Err = err
		return result
	}
	config.BaseURL = base
	request, err := config.ModelsRequest(ctx)
	if err != nil {
		result.Err = err
		return result
	}
	// Account identity is stable across token renewals. Credentials are never written to disk.
	path, err := catalogCachePath(catalog.getenv, result.Provider, request.URL.String(), request.Header.Get("ChatGPT-Account-ID"))
	if err == nil {
		result.cachePath = path
		if !force {
			if choices, err := readCatalogCache(path, result.Provider); err == nil {
				result.Choices, result.cachePath = choices, ""
				return result
			}
		}
	}
	client := *catalog.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		result.Err = fmt.Errorf("discover Codex models: %w", err)
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Err = fmt.Errorf("discover Codex models: HTTP %d", response.StatusCode)
		return result
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		result.Err = err
		return result
	}
	if len(data) > 8<<20 {
		result.Err = fmt.Errorf("model catalog from Codex exceeds 8 MiB")
		return result
	}
	var parsed struct {
		Models []struct {
			Slug          string `json:"slug"`
			ContextWindow int    `json:"context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		result.Err = fmt.Errorf("parse Codex catalog: %w", err)
		return result
	}
	for _, entry := range parsed.Models {
		if entry.Slug == "" {
			continue
		}
		choice := catalogChoice(result.Provider, entry.Slug, "discovered", "available")
		choice.ContextWindowTokens = max(0, entry.ContextWindow)
		result.Choices = append(result.Choices, choice)
	}
	return result
}
