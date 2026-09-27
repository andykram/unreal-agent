package openaicodex

import (
	"context"
	"net/http"
)

// ModelsRequest uses the same validated endpoint and subscription credentials as inference.
// Callers must refuse redirects, so subscription credentials stay on this endpoint.
func (config Config) ModelsRequest(ctx context.Context) (*http.Request, error) {
	base, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	credentials, err := config.credentials()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models?client_version=0.155.0", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.accessToken)
	request.Header.Set("ChatGPT-Account-ID", credentials.accountID)
	request.Header.Set("originator", "unreal-agent")
	request.Header.Set("User-Agent", "unreal-agent")
	return request, nil
}
