// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/promslog"
	"github.com/stretchr/testify/require"
)

func TestGetClientLogger(t *testing.T) {
	t.Run("returns nil when request is nil", func(t *testing.T) {
		logger := getClientLogger(nil, "test")
		require.Nil(t, logger, "expected nil logger when request is nil")
	})

	t.Run("returns nil when session is unavailable", func(t *testing.T) {
		// Create a request without a session (GetSession will return nil).
		// CallToolRequest is ServerRequest[*CallToolParamsRaw].
		req := &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{
				Name: "test-tool",
			},
		}

		logger := getClientLogger(req, "test")
		require.Nil(t, logger, "expected nil logger when session is unavailable")
	})

	// Raw POSTs give direct control of the protocol headers and _meta. The
	// stateless handler serves no standalone stream, so a notification only
	// arrives on the response of the request that made it.
	t.Run("delivers a handler's messages to the client that made the call", func(t *testing.T) {
		endpoint := newClientLoggingTestServer(t)

		testCases := []struct {
			name    string
			headers http.Header
			body    string
		}{
			{
				// The transport sets a log level for legacy clients itself.
				name:    "legacy client",
				headers: http.Header{"Mcp-Protocol-Version": {protocolVersionLegacy}},
				body:    `{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "reload", "arguments": {}}}`,
			},
			{
				// The 2026-07-28 revision dropped logging/setLevel, so a
				// modern client asks for a level per request instead. The
				// SDK sends nothing at all until one is set.
				name: "modern client",
				headers: http.Header{
					"Mcp-Protocol-Version": {protocolVersionModern},
					"Mcp-Method":           {"tools/call"},
					"Mcp-Name":             {"reload"},
				},
				body: `{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
					"name": "reload", "arguments": {},
					"_meta": {
						"io.modelcontextprotocol/protocolVersion": "` + protocolVersionModern + `",
						"io.modelcontextprotocol/clientCapabilities": {},
						"io.modelcontextprotocol/logLevel": "debug"
					}
				}}`,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				resp := postJSONRPC(t, endpoint, tc.headers, tc.body)
				defer resp.Body.Close()
				require.Equal(t, http.StatusOK, resp.StatusCode)

				// The response stream carries the notifications too.
				raw, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				got := string(raw)
				require.Contains(t, got, "triggering Prometheus configuration reload")
				require.Contains(t, got, "reload completed successfully")
			})
		}
	})
}

func TestGetChainedLogger(t *testing.T) {
	t.Run("returns nop logger when both loggers are nil", func(t *testing.T) {
		logger := getChainedLogger(nil, nil, "test")
		require.NotNil(t, logger, "expected non-nil logger even when both inputs are nil")

		// Verify logging doesn't panic (nop logger should silently discard).
		require.NotPanics(t, func() {
			logger.Info("test message")
		})
	})

	t.Run("returns app logger when request is nil", func(t *testing.T) {
		// Create a test logger that writes to a buffer so we can verify output.
		var buf bytes.Buffer
		appLogger := slog.New(slog.NewTextHandler(&buf, nil))

		logger := getChainedLogger(appLogger, nil, "test")
		require.NotNil(t, logger)

		// Log a message and verify it was captured.
		logger.Info("test message from app logger")
		output := buf.String()
		require.Contains(t, output, "test message from app logger",
			"expected log message to be captured by the app logger")
	})

	t.Run("returns app logger when session is unavailable", func(t *testing.T) {
		// Create a test logger that writes to a buffer.
		var buf bytes.Buffer
		appLogger := slog.New(slog.NewTextHandler(&buf, nil))

		// CallToolRequest is ServerRequest[*CallToolParamsRaw].
		req := &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{
				Name: "test-tool",
			},
		}

		logger := getChainedLogger(appLogger, req, "test")
		require.NotNil(t, logger)

		// Log a message and verify it was captured by the app logger.
		logger.Info("test message with unavailable session")
		output := buf.String()
		require.Contains(t, output, "test message with unavailable session",
			"expected log message to be captured by the app logger")
	})
}

// newClientLoggingTestServer serves an MCP server with client logging
// enabled behind the stateless HTTP handler, in front of a backend that
// accepts every management API call, and returns the URL it listens on.
func newClientLoggingTestServer(t *testing.T) string {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	server, _, err := NewServer(context.Background(), ServerConfig{
		Logger:               promslog.NewNopLogger(),
		PrometheusURL:        backend.URL,
		PrometheusTimeout:    10 * time.Second,
		RoundTripper:         http.DefaultTransport,
		EnabledTools:         []string{"all"},
		ClientLoggingEnabled: true,
		Transport:            TransportHTTP,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(NewStreamableHTTPHandler(server, promslog.NewNopLogger()))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}

// postJSONRPC sends one raw JSON-RPC message with the headers the streamable
// HTTP transport requires and returns the response. The caller closes the
// body.
func postJSONRPC(t *testing.T, url string, extraHeaders http.Header, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, vs := range extraHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
