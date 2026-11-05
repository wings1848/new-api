package middleware

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/privacy"

	"github.com/gin-gonic/gin"
)

// Response restore (issue #11): the inbound filter (issue #10) stores the
// placeholder -> original value map on the context. This middleware wraps the
// gin response writer so the client sees the real values again, for plain JSON
// responses and streaming SSE alike. A placeholder echoed by a model arrives
// token by token, so SSE frames may carry only a fragment of it; the writer
// reassembles the fragments before restoring (issue: streamed restore).

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

	// SSE state: frames are inspected as JSON strings because a placeholder may
	// be split across frames, with JSON syntax between the fragments.
	sseChecked bool
	sse        bool
	prefixes   map[string]struct{}
	carry      string
	heldFrames []heldSSEFrame
}

// heldSSEFrame is one SSE frame buffered because it ends with a placeholder
// prefix. trimPrefix marks bytes of the matched string that continue a carried
// fragment; trimSuffix marks bytes that start one.
type heldSSEFrame struct {
	frame      []byte
	start      int
	end        int
	trimPrefix int
	trimSuffix int
}

func newPrivacyRestoreWriter(writer gin.ResponseWriter, replacements map[string]string) *privacyRestoreWriter {
	maxPlaceholder := 0
	prefixes := make(map[string]struct{}, len(replacements)*8)
	for placeholder := range replacements {
		if len(placeholder) > maxPlaceholder {
			maxPlaceholder = len(placeholder)
		}
		for length := 1; length <= len(placeholder); length++ {
			prefixes[placeholder[:length]] = struct{}{}
		}
	}
	return &privacyRestoreWriter{
		ResponseWriter: writer,
		replacements:   replacements,
		maxPlaceholder: maxPlaceholder,
		prefixes:       prefixes,
	}
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

func (w *privacyRestoreWriter) isSSE() bool {
	if w.sseChecked {
		return w.sse
	}
	w.sseChecked = true
	contentType := strings.ToLower(w.ResponseWriter.Header().Get("Content-Type"))
	w.sse = strings.HasPrefix(contentType, "text/event-stream")
	return w.sse
}

func (w *privacyRestoreWriter) drain(final bool) error {
	if w.isSSE() {
		return w.drainSSE(final)
	}
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

func (w *privacyRestoreWriter) drainSSE(final bool) error {
	for {
		index, delimiter := findSSEBlock(w.pending)
		if index < 0 {
			break
		}
		block := append([]byte(nil), w.pending[:index+delimiter]...)
		w.pending = append(w.pending[:0], w.pending[index+delimiter:]...)
		if err := w.emit(block, final); err != nil {
			return err
		}
	}
	if !final {
		return nil
	}
	if len(w.pending) > 0 {
		block := append([]byte(nil), w.pending...)
		w.pending = nil
		if err := w.emit(block, true); err != nil {
			return err
		}
	}
	w.carry = ""
	held := w.flushHeldFrames(false)
	w.heldFrames = nil
	return w.write(held)
}

func (w *privacyRestoreWriter) emit(block []byte, final bool) error {
	return w.write(w.handleSSEBlock(block, final))
}

func (w *privacyRestoreWriter) write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(data)
	return err
}

// handleSSEBlock processes one complete SSE frame and returns the bytes that
// are safe to send right away (nothing while a placeholder fragment is held).
func (w *privacyRestoreWriter) handleSSEBlock(block []byte, final bool) []byte {
	if len(w.carry) > 0 {
		if final {
			out := w.flushHeldFrames(false)
			w.carry = ""
			return append(out, w.restoreBlock(block)...)
		}
		if out, ok := w.continueCarry(block); ok {
			return out
		}
		out := w.flushHeldFrames(false)
		w.carry = ""
		return append(out, w.handleFreshBlock(block, final)...)
	}
	return w.handleFreshBlock(block, final)
}

func (w *privacyRestoreWriter) handleFreshBlock(block []byte, final bool) []byte {
	if !final {
		if suffix, start, end, ok := detectSSEFragmentSuffix(block, w.prefixes, w.maxPlaceholder); ok {
			if _, complete := w.replacements[suffix]; !complete {
				w.heldFrames = append(w.heldFrames, heldSSEFrame{
					frame:      block,
					start:      start,
					end:        end,
					trimSuffix: len(suffix),
				})
				w.carry = suffix
				return nil
			}
		}
	}
	return w.restoreBlock(block)
}

// continueCarry looks for the continuation of a held placeholder in the next
// frame. It returns ok=false when the frame does not continue the fragment, in
// which case the caller flushes the held frames unchanged.
func (w *privacyRestoreWriter) continueCarry(block []byte) ([]byte, bool) {
	spans := jsonStringSpans(block)
	if len(spans) == 0 {
		// Comments and pings may arrive between fragments; keep holding.
		return block, true
	}
	for _, span := range spans {
		content := block[span[0]:span[1]]
		candidate := w.carry + string(content)
		if real, rest, ok := resolvePlaceholder(candidate, w.replacements, w.maxPlaceholder); ok {
			used := len(content) - len(rest)
			newContent := append(jsonStringContent(real), content[used:]...)
			edited := spliceJSONStringSpan(block, span[0], span[1], newContent)
			out := w.flushHeldFrames(true)
			w.carry = ""
			return append(out, w.handleFreshBlock(edited, false)...), true
		}
		if len(candidate) <= w.maxPlaceholder {
			if _, partial := w.prefixes[candidate]; partial {
				w.heldFrames = append(w.heldFrames, heldSSEFrame{
					frame:      block,
					start:      span[0],
					end:        span[1],
					trimPrefix: len(content),
				})
				w.carry = candidate
				return nil, true
			}
		}
	}
	return nil, false
}

// flushHeldFrames returns the buffered frames in order. When resolved is true
// the carried fragments are removed (and complete placeholders restored), so
// the client sees the real value in the continuation frame instead.
func (w *privacyRestoreWriter) flushHeldFrames(resolved bool) []byte {
	var out []byte
	for _, held := range w.heldFrames {
		frame := held.frame
		if resolved {
			content := frame[held.start:held.end]
			trimPrefix := min(held.trimPrefix, len(content))
			trimSuffix := min(held.trimSuffix, len(content)-trimPrefix)
			content = content[trimPrefix : len(content)-trimSuffix]
			frame = spliceJSONStringSpan(frame, held.start, held.end, content)
		}
		out = append(out, w.restoreBlock(frame)...)
	}
	w.heldFrames = nil
	return out
}

func (w *privacyRestoreWriter) restoreBlock(block []byte) []byte {
	return []byte(privacy.Restore(string(block), w.replacements))
}

// findSSEBlock returns where the first complete SSE frame ends and the length
// of its separator. Frames end with a blank line ("\n\n" or "\r\n\r\n").
func findSSEBlock(data []byte) (int, int) {
	newline := bytes.Index(data, []byte("\n\n"))
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	switch {
	case newline < 0 && crlf < 0:
		return -1, 0
	case crlf < 0:
		return newline, 2
	case newline < 0:
		return crlf, 4
	case newline <= crlf:
		return newline, 2
	default:
		return crlf, 4
	}
}

// jsonStringSpans lists the byte ranges of the string values inside an SSE
// frame (start is the first byte after the quote, end is the closing quote).
// Object keys are skipped: a key is followed by a colon, and only values can
// carry streamed text.
func jsonStringSpans(data []byte) [][2]int {
	var spans [][2]int
	inString := false
	start := 0
	for index := 0; index < len(data); index++ {
		char := data[index]
		if !inString {
			if char == '"' {
				inString = true
				start = index + 1
			}
			continue
		}
		if char == '\\' {
			index++
			continue
		}
		if char != '"' {
			continue
		}
		inString = false
		next := index + 1
		for next < len(data) && (data[next] == ' ' || data[next] == '\t') {
			next++
		}
		if next < len(data) && data[next] == ':' {
			continue
		}
		spans = append(spans, [2]int{start, index})
	}
	return spans
}

// detectSSEFragmentSuffix reports a trailing placeholder prefix in the last
// string of a frame: the frame must be held until the continuation arrives.
func detectSSEFragmentSuffix(block []byte, prefixes map[string]struct{}, maxPlaceholder int) (string, int, int, bool) {
	spans := jsonStringSpans(block)
	for index := len(spans) - 1; index >= 0; index-- {
		span := spans[index]
		content := block[span[0]:span[1]]
		limit := min(len(content), maxPlaceholder)
		for length := limit; length > 0; length-- {
			suffix := string(content[len(content)-length:])
			if _, ok := prefixes[suffix]; ok {
				return suffix, span[0], span[1], true
			}
		}
	}
	return "", 0, 0, false
}

// resolvePlaceholder restores the value when candidate starts with a complete
// placeholder and returns the text that follows it.
func resolvePlaceholder(candidate string, replacements map[string]string, maxPlaceholder int) (string, string, bool) {
	limit := min(len(candidate), maxPlaceholder)
	for length := limit; length > 0; length-- {
		if real, ok := replacements[candidate[:length]]; ok {
			return real, candidate[length:], true
		}
	}
	return "", "", false
}

// jsonStringContent encodes a value for insertion inside a JSON string.
func jsonStringContent(value string) []byte {
	encoded, err := common.Marshal(value)
	if err != nil {
		return []byte(value)
	}
	return encoded[1 : len(encoded)-1]
}

func spliceJSONStringSpan(block []byte, start int, end int, content []byte) []byte {
	result := make([]byte, 0, len(block)-(end-start)+len(content))
	result = append(result, block[:start]...)
	result = append(result, content...)
	result = append(result, block[end:]...)
	return result
}

// splitPrivacyPending splits the buffer into bytes safe to send now and a tail
// that may still grow into a placeholder. A "{{" tail longer than the longest
// placeholder cannot be one of ours and is released, so a model streaming
// literal braces cannot grow the buffer without bound. This byte-level path is
// used for non-SSE responses; SSE frames are reassembled in drainSSE.
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
