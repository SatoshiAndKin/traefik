package httputil

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// requestBodyProgress tracks completion without closing a body that the transport is still reading.
type requestBodyProgress struct {
	io.ReadCloser

	remaining int64
	complete  atomic.Bool
}

func (b *requestBodyProgress) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.remaining > 0 {
		b.remaining -= int64(n)
		if b.remaining == 0 {
			b.complete.Store(true)
		}
	}
	if err != nil {
		b.complete.Store(true)
	}
	return n, err
}

// earlyResponseWriter prevents request-body cleanup from withholding an early reply.
// Completed uploads retain the configured response buffering.
type earlyResponseWriter struct {
	http.ResponseWriter

	request  http.Request
	body     requestBodyProgress
	writeErr error
}

func (w *earlyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *earlyResponseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	// Flushing an informational response would commit an implicit final 200.
	if status >= http.StatusOK {
		w.writeErr = w.flushEarly()
	}
}

func (w *earlyResponseWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	n, err := w.ResponseWriter.Write(p)
	if err == nil {
		err = w.flushEarly()
	}
	return n, err
}

func (w *earlyResponseWriter) flushEarly() error {
	if w.body.complete.Load() {
		return nil
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
