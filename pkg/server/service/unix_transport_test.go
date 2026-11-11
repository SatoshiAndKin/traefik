//go:build unix

package service

import (
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v3/pkg/config/dynamic"
)

func TestUnixSocketTransport(t *testing.T) {
	// Keep the socket address within the kernel limit on macOS.
	t.Setenv("GOTMPDIR", "/tmp")
	path := filepath.Join(t.TempDir(), "http.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer listener.Close()

	var accepted atomic.Int32
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "canary.invalid", r.Host)
			assert.Contains(t, []string{"/health", "/fastest"}, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				accepted.Add(1)
			}
		},
	}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()

	cfg := &dynamic.ServersTransport{
		UnixSocket:          path,
		ForwardingTimeouts:  &dynamic.ForwardingTimeouts{},
		MaxIdleConnsPerHost: 32,
	}
	cfg.ForwardingTimeouts.SetDefaults()
	manager := &TransportManager{}
	transport, err := manager.createRoundTripper(cfg, nil)
	require.NoError(t, err)
	for _, path := range []string{"/health", "/fastest"} {
		request, err := http.NewRequest(http.MethodGet, "http://canary.invalid"+path, nil)
		require.NoError(t, err)
		response, err := transport.RoundTrip(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, http.StatusNoContent, response.StatusCode)
	}
	assert.EqualValues(t, 1, accepted.Load())
}

func TestUnixSocketTransportRequiresAbsolutePath(t *testing.T) {
	manager := &TransportManager{}
	_, err := manager.createRoundTripper(&dynamic.ServersTransport{UnixSocket: "relative.sock"}, nil)
	require.ErrorContains(t, err, "absolute path")
}
