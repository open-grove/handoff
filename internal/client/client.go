package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/open-grove/handoff/internal/types"
)

type Client struct {
	Server      string
	Token       string
	DeleteToken string
	HTTP        *http.Client
}

func (client Client) Publish(ctx context.Context, input types.PublishRequest) (types.CreateResponse, error) {
	var output types.CreateResponse
	err := client.request(ctx, http.MethodPost, "/v1/handoffs", input, &output)
	return output, err
}

func (client Client) Get(ctx context.Context, id string) (types.CreateResponse, error) {
	var output types.CreateResponse
	err := client.request(ctx, http.MethodGet, "/v1/handoffs/"+id, nil, &output)
	return output, err
}

func (client Client) GetContext(ctx context.Context, id string) (types.ContextResponse, error) {
	var output types.ContextResponse
	err := client.request(ctx, http.MethodGet, "/v1/handoffs/"+id+"/context", nil, &output)
	return output, err
}

func (client Client) Delete(ctx context.Context, id string) error {
	return client.request(ctx, http.MethodDelete, "/v1/handoffs/"+id, nil, nil)
}

func (client Client) Health(ctx context.Context) (map[string]any, error) {
	var output map[string]any
	err := client.request(ctx, http.MethodGet, "/healthz", nil, &output)
	return output, err
}

func (client Client) request(ctx context.Context, method, path string, input, output any) error {
	if err := validateServer(client.Server); err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.Server, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+client.Token)
	}
	if client.DeleteToken != "" {
		request.Header.Set("X-Handoff-Delete-Token", client.DeleteToken)
	}
	httpClient := client.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError types.ErrorResponse
		if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&apiError) == nil && apiError.Error != "" {
			return fmt.Errorf("server: %s", apiError.Error)
		}
		return fmt.Errorf("server returned HTTP %d", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(output)
}

func validateServer(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("server must be an absolute URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("remote handoff servers must use HTTPS")
}
