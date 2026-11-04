package middleware

import (
	"bytes"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/privacy"

	"github.com/gin-gonic/gin"
)

// Response restore (issue #11): the inbound filter (issue #10) stores the
// placeholder -> original value map on the context. This middleware wraps the
// gin response writer so the client sees the real values again, for plain JSON
// responses and streaming SSE alike, including a placeholder split across
// chunks (a possible placeholder prefix is held back until it completes).

// PrivacyResponseRestore restores placeholders in the response body when the
// inbound filter masked this request; requests without replacements are passed
// through unwrapped. The inbound middleware installs the same writer itself
// right after masking, so production traffic does not need this extra handler.
func PrivacyResponseRestore() gin.HandlerFunc {
	return func(c *gin.Context) {
		value, ok := common.GetContextKey(c, constant.ContextKeyPrivacyReplacements)
		if !ok {
			c.Next()
			return
		}
		replacements, _ := value.(map[string]string)
		if len(replacements) == 0 {
			c.Next()
			return
		}
		finish := installPrivacyRestoreWriter(c, replacements)
		defer finish()
		c.Next()
	}
}

// installPrivacyRestoreWriter swaps c.Writer for the restoring wrapper and
// returns a cleanup that flushes the remaining buffer and restores the writer.
func installPrivacyRestoreWriter(c *gin.Context, replacements map[string]string) func() {
	original := c.Writer
	writer := newPrivacyRestoreWriter(original, replacements)
	c.Writer = writer
	return func() {
		writer.Finish()
		c.Writer = original
	}
}

type privacyRestoreWriter struct {
	gin.ResponseWriter
	replacements   map[string]string
	maxPlaceholder int
	pending        []byte
}

func newPrivacyRestoreWriter(writer gin.ResponseWriter, replacements map[string]string) *privacyRestoreWriter {
	maxPlaceholder := 0
	for placeholder := range replacements {
		if len(placeholder) > maxPlaceholder {
			maxPlaceholder = len(placeholder)
		}
	}
	return &privacyRestoreWriter{ResponseWriter: writer, replacements: replacements, maxPlaceholder: maxPlaceholder}
}

func (w *privacyRestoreWriter) Write(data []byte) (int, error) {
	w.pending = append(w.pending, data...)
	if err := w.drain(false); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *privacyRestoreWriter) WriteString(value string) (int, error) {
	w.pending = append(w.pending, value...)
	if err := w.drain(false); err != nil {
		return 0, err
	}
	return len(value), nil
}

// Flush sends everything that cannot be part of a placeholder and then flushes
// the underlying writer; a partial placeholder stays buffered for the next
// chunk (issue #11: SSE chunks may split a placeholder anywhere).
func (w *privacyRestoreWriter) Flush() {
	_ = w.drain(false)
	w.ResponseWriter.Flush()
}

// Finish flushes the remaining buffer, including a never-completed "{{" tail.
func (w *privacyRestoreWriter) Finish() {
	_ = w.drain(true)
	w.ResponseWriter.Flush()
}

// WriteHeader drops an explicit Content-Length: restoring placeholders changes
// the body length, and a stale value would truncate the client response.
func (w *privacyRestoreWriter) WriteHeader(code int) {
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}

func (w *privacyRestoreWriter) WriteHeaderNow() {
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeaderNow()
}

// Unwrap lets http.ResponseController (write deadlines, flush-error discovery)
// reach the gin writer underneath this restore layer.
func (w *privacyRestoreWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *privacyRestoreWriter) drain(final bool) error {
	emit, hold := splitPrivacyPending(w.pending, w.maxPlaceholder, final)
	if len(emit) > 0 {
		restored := privacy.Restore(string(emit), w.replacements)
		if _, err := w.ResponseWriter.Write([]byte(restored)); err != nil {
			return err
		}
	}
	if len(hold) == 0 {
		w.pending = nil
	} else {
		w.pending = append(w.pending[:0], hold...)
	}
	return nil
}

// splitPrivacyPending splits the buffer into bytes safe to send now and a tail
// that may still grow into a placeholder. A "{{" tail longer than the longest
// placeholder cannot be one of ours and is released, so a model streaming
// literal braces cannot grow the buffer without bound.
func splitPrivacyPending(pending []byte, maxPlaceholder int, final bool) (emit []byte, hold []byte) {
	if final || len(pending) == 0 || maxPlaceholder <= 0 {
		return pending, nil
	}
	lastOpen := bytes.LastIndex(pending, []byte("{{"))
	if lastOpen >= 0 {
		tail := pending[lastOpen:]
		if !bytes.Contains(tail, []byte("}}")) {
			if len(tail) > maxPlaceholder {
				return pending, nil
			}
			return pending[:lastOpen], tail
		}
	}
	if pending[len(pending)-1] == '{' {
		return pending[:len(pending)-1], pending[len(pending)-1:]
	}
	return pending, nil
}
