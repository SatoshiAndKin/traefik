package httputil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v3/pkg/testhelpers"
)

func TestProxy_ResponseBeforeFinalRequestEOF(t *testing.T) {
	t.Parallel()

	requestBody := strings.Repeat("request", 4096)
	responseBody := strings.Repeat("response", 16384)
	backend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, err := io.CopyN(io.Discard, req.Body, int64(len(requestBody)))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		rw.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		_, _ = io.WriteString(rw, responseBody)
	}))
	t.Cleanup(backend.Close)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	proxy := buildSingleHostProxy(testhelpers.MustParseURL(backend.URL), true, false, 0, transport, newBufferPool())
	eofResult := make(chan error, 1)
	frontend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		responseStarted := make(chan struct{})
		eofChecked := make(chan struct{})
		req.Body = &finalEOFBody{
			ReadCloser:      req.Body,
			remaining:       req.ContentLength,
			ctx:             req.Context(),
			responseStarted: responseStarted,
			eofChecked:      eofChecked,
			eofResult:       eofResult,
		}
		proxy.ServeHTTP(&firstWriteBarrier{
			ResponseWriter:  rw,
			responseStarted: responseStarted,
			eofChecked:      eofChecked,
		}, req)
	}))
	t.Cleanup(frontend.Close)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(frontend.URL, "application/json", strings.NewReader(requestBody))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, responseBody, string(body))
	select {
	case err := <-eofResult:
		assert.ErrorIs(t, err, io.EOF)
	case <-t.Context().Done():
		t.Fatal("final request EOF was not checked")
	}
}

// finalEOFBody holds the transport's final EOF check until response writing has
// started, so HTTP/1's automatic request-body close cannot hide behind timing.
type finalEOFBody struct {
	io.ReadCloser

	remaining       int64
	ctx             context.Context
	responseStarted <-chan struct{}
	eofChecked      chan struct{}
	eofResult       chan<- error
}

func (b *finalEOFBody) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		n, err := b.ReadCloser.Read(p)
		b.remaining -= int64(n)
		if n > 0 && err == io.EOF {
			err = nil
		}
		return n, err
	}

	select {
	case <-b.responseStarted:
	case <-b.ctx.Done():
		b.eofResult <- b.ctx.Err()
		close(b.eofChecked)
		return 0, b.ctx.Err()
	}
	n, err := b.ReadCloser.Read(p)
	b.eofResult <- err
	close(b.eofChecked)
	return n, err
}

type firstWriteBarrier struct {
	http.ResponseWriter

	once            sync.Once
	responseStarted chan struct{}
	eofChecked      <-chan struct{}
}

func (w *firstWriteBarrier) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.once.Do(func() {
		close(w.responseStarted)
		<-w.eofChecked
	})
	return n, err
}

func (w *firstWriteBarrier) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
