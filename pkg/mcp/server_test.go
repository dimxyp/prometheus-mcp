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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/promslog"
	"github.com/stretchr/testify/require"
)

// The go-sdk does not export its protocol version strings.
const (
	protocolVersionModern = "2026-07-28"
)

// newTestHTTPServer starts the MCP server behind the streamable HTTP
// handler. Tool calls are never made, so no backend is needed.
func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	logger := promslog.NewNopLogger()
	server, _, err := NewServer(context.Background(), ServerConfig{
		Logger:        logger,
		PrometheusURL: "http://127.0.0.1:9090",
		RoundTripper:  http.DefaultTransport,
		Transport:     TransportHTTP,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(NewStreamableHTTPHandler(server, logger))
	t.Cleanup(httpServer.Close)
	return httpServer
}

// TestStreamableHTTPHandler_NegotiatesModernProtocol pins what the handler
// hands a 2026-07-28 client: the revision and the instructions blob.
func TestStreamableHTTPHandler_NegotiatesModernProtocol(t *testing.T) {
	t.Parallel()

	httpServer := newTestHTTPServer(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)

	ctx := context.Background()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	defer session.Close()

	require.Equal(t, protocolVersionModern, session.InitializeResult().ProtocolVersion)

	require.NotEmpty(t, session.InitializeResult().Instructions)

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)
}
