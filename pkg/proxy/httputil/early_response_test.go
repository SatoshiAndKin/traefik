package httputil

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v3/pkg/testhelpers"
)

func TestProxy_EarlyResponse(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		for _, body := range []string{"", "request rejected"} {
			t.Run(fmt.Sprintf("chunked=%v/body=%v", chunked, body != ""), func(t *testing.T) {
				t.Parallel()

				backend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
					if err := http.NewResponseController(rw).EnableFullDuplex(); err != nil {
						http.Error(rw, err.Error(), http.StatusInternalServerError)
						return
					}
					if _, err := io.CopyN(io.Discard, req.Body, 1); err != nil {
						http.Error(rw, err.Error(), http.StatusBadRequest)
						return
					}
					rw.WriteHeader(http.StatusEarlyHints)
					rw.Header().Set("Content-Length", strconv.Itoa(len(body)))
					rw.WriteHeader(http.StatusRequestEntityTooLarge)
					_, _ = io.WriteString(rw, body)
					_ = http.NewResponseController(rw).Flush()
				}))
				t.Cleanup(backend.Close)

				transport := http.DefaultTransport.(*http.Transport).Clone()
				t.Cleanup(transport.CloseIdleConnections)
				proxy := buildSingleHostProxy(testhelpers.MustParseURL(backend.URL), true, false, 0, transport, newBufferPool())
				frontend := httptest.NewServer(proxy)
				t.Cleanup(frontend.Close)

				conn, err := net.DialTimeout("tcp", frontend.Listener.Addr().String(), time.Second)
				require.NoError(t, err)
				t.Cleanup(func() { _ = conn.Close() })
				require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
				framing := "Content-Length: 1048576\r\n"
				if chunked {
					framing = "Transfer-Encoding: chunked\r\n"
				}
				_, err = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: proxy\r\n%s\r\n", framing)
				require.NoError(t, err)
				if chunked {
					_, err = io.WriteString(conn, "4000\r\n"+strings.Repeat("x", 16384)+"\r\n")
				} else {
					_, err = io.WriteString(conn, strings.Repeat("x", 16384))
				}
				require.NoError(t, err)

				// Neither the declared body nor the chunked terminator is sent.
				reader := bufio.NewReader(conn)
				response, err := http.ReadResponse(reader, nil)
				require.NoError(t, err)
				assert.Equal(t, http.StatusEarlyHints, response.StatusCode)
				require.NoError(t, response.Body.Close())
				response, err = http.ReadResponse(reader, nil)
				require.NoError(t, err)
				assert.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
				actual, err := io.ReadAll(response.Body)
				assert.NoError(t, err)
				assert.Equal(t, body, string(actual))
				require.NoError(t, response.Body.Close())
			})
		}
	}
}

func TestProxy_CompletedUploadKeepsResponseBuffering(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		rw.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(rw, "ok")
	}))
	t.Cleanup(backend.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	proxy := buildSingleHostProxy(testhelpers.MustParseURL(backend.URL), true, false, 0, transport, newBufferPool())
	var flushes atomic.Int64
	frontend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		proxy.ServeHTTP(&countedFlushWriter{ResponseWriter: rw, flushes: &flushes}, req)
	}))
	t.Cleanup(frontend.Close)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Post(frontend.URL, "application/json", strings.NewReader("request"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
	assert.Zero(t, flushes.Load())
}

type countedFlushWriter struct {
	http.ResponseWriter

	flushes *atomic.Int64
}

func (w *countedFlushWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *countedFlushWriter) FlushError() error {
	w.flushes.Add(1)
	return http.NewResponseController(w.ResponseWriter).Flush()
}
