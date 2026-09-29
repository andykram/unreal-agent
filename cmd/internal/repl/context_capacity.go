package repl

import "github.com/unreallabsai/unreal-agent/cmd/internal/agentrunner"

func (model *uiModel) contextCapacity() (int, string) {
	settings := model.config.Current()
	if settings.Model.ContextWindowTokens > 0 {
		return settings.Model.ContextWindowTokens, "configured"
	}
	ref := ModelRef{Provider: settings.Model.Provider, ID: settings.Model.ID}
	if model.router != nil {
		ref = model.router.Selected()
	}
	if ref.ID == "" {
		for _, provider := range agentrunner.DefaultProviders() {
			if provider.Name == ref.Provider {
				ref.ID = provider.DefaultModel
				break
			}
		}
	}
	if limit := settings.Providers[ref.Provider].ContextWindows[ref.ID]; limit > 0 {
		return limit, "configured model"
	}
	if limit := model.catalog.ContextWindow(ref); limit > 0 {
		return limit, "provider catalog"
	}
	if ref.Provider == "openai" && settings.Providers[ref.Provider].BaseURL == "" {
		// Exact model IDs documented at https://developers.openai.com/api/docs/models.
		switch ref.ID {
		case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna":
			return 1_050_000, "OpenAI model guide"
		}
	}
	return 0, ""
}
