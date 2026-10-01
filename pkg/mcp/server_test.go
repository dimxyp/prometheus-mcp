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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/common/promslog"
	"github.com/stretchr/testify/require"
)

// The go-sdk does not export its protocol version strings.
const (
	protocolVersionModern = "2026-07-28"
	protocolVersionLegacy = "2025-11-25"
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
// hands a 2026-07-28 client: the revision, the instructions blob, and the
// advertised capabilities.
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

	caps := session.InitializeResult().Capabilities
	require.NotNil(t, caps.Tools)
	require.False(t, caps.Tools.ListChanged, "tools must not advertise listChanged")
	require.NotNil(t, caps.Prompts)
	require.False(t, caps.Prompts.ListChanged, "prompts must not advertise listChanged")
	require.NotNil(t, caps.Resources)
	require.False(t, caps.Resources.ListChanged, "resources must not advertise listChanged")

	tools, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)
}

// syncBuffer is a bytes.Buffer safe for concurrent use; the go-sdk logs from
// its own session goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestSDKLoggerReceivesTheSDKRecords pins the split between the two loggers.
// "server session connected" is written synchronously by every Connect.
func TestSDKLoggerReceivesTheSDKRecords(t *testing.T) {
	t.Parallel()

	var appBuf, sdkBuf syncBuffer
	server, _, err := NewServer(context.Background(), ServerConfig{
		Logger:        promslog.New(&promslog.Config{Writer: &appBuf}),
		SDKLogger:     promslog.New(&promslog.Config{Writer: &sdkBuf}),
		PrometheusURL: "http://127.0.0.1:9090",
		RoundTripper:  http.DefaultTransport,
		Transport:     TransportStdio,
	})
	require.NoError(t, err)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	_, err = server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	const sdkRecord = "server session connected"
	require.Contains(t, sdkBuf.String(), sdkRecord, "the SDK's records must reach SDKLogger")
	require.NotContains(t, appBuf.String(), sdkRecord, "the SDK's records must not reach Logger")

	const appRecord = "MCP server created"
	require.Contains(t, appBuf.String(), appRecord, "the server's records must reach Logger")
	require.NotContains(t, sdkBuf.String(), appRecord, "the server's records must not reach SDKLogger")
}
