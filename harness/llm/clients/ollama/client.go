package ollama

import (
	"errors"
	"net"
	"net/http"
	"net/url"
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
		endpoint, err := url.Parse(baseURL)
		if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return nil, errors.New("invalid authenticated Ollama base URL")
		}
		loopback := strings.EqualFold(endpoint.Hostname(), "localhost") || net.ParseIP(endpoint.Hostname()).IsLoopback()
		if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
			return nil, errors.New("ollama API keys require HTTPS except for loopback HTTP")
		}
		headers["Authorization"] = []string{"Bearer " + config.APIKey}
	}
	var remote *primitives.RemoteClient
	if config.APIKey != "" {
		// A redirect must not bypass the credential-bearing endpoint policy.
		remote = primitives.NewRemoteClientWithHTTPClient(&http.Client{
			Transport:     http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		})
	} else {
		remote = primitives.NewRemoteClient()
	}
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
