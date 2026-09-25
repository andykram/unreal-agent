package ollama

import (
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

const BaseURL = "http://localhost:11434/v1"

type Config struct {
	BaseURL     string
	APIKey      string
	MaxAttempts *int
}

type Client struct {
	llm.Adapter
	remote *primitives.RemoteClient
}

func NewClient(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = BaseURL
	}
	headers := map[string][]string{"Content-Type": {"application/json"}}
	if config.APIKey != "" {
		headers["Authorization"] = []string{"Bearer " + config.APIKey}
	}
	remote := primitives.NewRemoteClient()
	adapter, err := responsesapi.NewAdapter(remote, responsesapi.Config{
		Endpoint:    baseURL + "/responses",
		Headers:     headers,
		MaxAttempts: config.MaxAttempts,
	})
	if err != nil {
		_ = remote.Close()
		return nil, err
	}
	return &Client{Adapter: adapter, remote: remote}, nil
}

func (client *Client) Close() error {
	return client.remote.Close()
}
