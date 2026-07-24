package client

import (
	"bufio"
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

func (client Client) CompactOnServer(ctx context.Context, input types.CompactRequest) (types.CreateResponse, error) {
	var output types.CreateResponse
	err := client.request(ctx, http.MethodPost, "/v1/handoffs/compact", input, &output)
	return output, err
}

func (client Client) PreviewServerCompaction(ctx context.Context, input types.CompactRequest) (types.CompactPreviewResponse, error) {
	var output types.CompactPreviewResponse
	err := client.requestWithAccept(ctx, http.MethodPost, "/v1/handoffs/compact-preview", input, &output, "text/event-stream, application/json")
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
	return client.requestWithAccept(ctx, method, path, input, output, "application/json")
}

func (client Client) requestWithAccept(ctx context.Context, method, path string, input, output any, accept string) error {
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
	request.Header.Set("Accept", accept)
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
		if strings.Contains(accept, "text/event-stream") {
			// The caller's context bounds a healthy long-running model stream.
			httpClient = &http.Client{}
		} else {
			httpClient = &http.Client{Timeout: 5 * time.Minute}
		}
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
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		preview, ok := output.(*types.CompactPreviewResponse)
		if !ok {
			return errors.New("unexpected event stream response")
		}
		return decodeCompactPreviewStream(response.Body, preview)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(output)
}

func decodeCompactPreviewStream(reader io.Reader, output *types.CompactPreviewResponse) error {
	buffered := bufio.NewReader(io.LimitReader(reader, 8<<20))
	event := ""
	data := make([]string, 0, 1)
	resultSeen := false
	consume := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		switch event {
		case "result":
			if err := json.Unmarshal([]byte(payload), output); err != nil {
				return fmt.Errorf("decode server compaction result: %w", err)
			}
			resultSeen = true
		case "error":
			var apiError types.ErrorResponse
			if json.Unmarshal([]byte(payload), &apiError) == nil && apiError.Error != "" {
				return fmt.Errorf("server compaction stream: %s", apiError.Error)
			}
			return errors.New("server compaction stream failed")
		}
		event = ""
		return nil
	}

	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			switch {
			case line == "":
				if consumeErr := consume(); consumeErr != nil {
					return consumeErr
				}
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if consumeErr := consume(); consumeErr != nil {
				return consumeErr
			}
			break
		}
	}
	if !resultSeen {
		return errors.New("server compaction stream ended without a result")
	}
	return nil
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
