package repl

import (
	"fmt"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// responseProvenance associates provider items with the response that produced
// them. An unrecognized or ambiguous item is treated as provider independent.
type responseProvenance struct {
	mu      sync.Mutex
	state   *SessionState
	items   map[string]string
	warning error
}

func newResponseProvenance(state *SessionState) *responseProvenance {
	return &responseProvenance{state: state, items: make(map[string]string)}
}

func providerItemKey(item llm.Item) string {
	if item.ProviderID == "" {
		return ""
	}
	return string(item.Type) + "\x00" + item.ProviderID
}

func (provenance *responseProvenance) index(response llm.Response, origin string) {
	if origin == "" {
		return
	}
	provenance.mu.Lock()
	defer provenance.mu.Unlock()
	for _, item := range response.Output {
		key := providerItemKey(item)
		if key == "" {
			continue
		}
		if previous, exists := provenance.items[key]; exists && previous != origin {
			provenance.items[key] = ""
		} else if !exists {
			provenance.items[key] = origin
		}
	}
}

func (provenance *responseProvenance) record(response llm.Response, origin string) {
	if response.ID == "" {
		return
	}
	if err := provenance.state.RecordResponseOrigin(response.ID, origin); err != nil {
		provenance.mu.Lock()
		provenance.warning = fmt.Errorf("could not save model response provenance: %w", err)
		provenance.mu.Unlock()
		return
	}
	provenance.index(response, origin)
}

func (provenance *responseProvenance) takeWarning() error {
	provenance.mu.Lock()
	defer provenance.mu.Unlock()
	warning := provenance.warning
	provenance.warning = nil
	return warning
}

func (provenance *responseProvenance) project(request llm.Request, origin string) llm.Request {
	provenance.mu.Lock()
	defer provenance.mu.Unlock()
	input := make([]llm.Item, 0, len(request.Input))
	for _, original := range request.Input {
		item := original
		known := provenance.items[providerItemKey(item)]
		if item.ProviderID == "" {
			known = ""
		}
		if item.Type == llm.ItemReasoning {
			if known == origin {
				input = append(input, item)
			}
			continue
		}
		if known != origin || item.Type == llm.ItemToolResult {
			item.ProviderID = ""
		}
		input = append(input, item)
	}
	request.Input = input
	return request
}

type provenanceBuilder struct {
	contextbuilder.Builder
	provenance *responseProvenance
}

func (builder *provenanceBuilder) AddModelResponse(response llm.Response) {
	builder.Builder.AddModelResponse(response)
	builder.provenance.index(response, builder.provenance.state.ResponseOrigin(response.ID))
}
