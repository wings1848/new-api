package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const privacyRestorePlaceholder = "{{WINGS_API_KEY_0123456789abcdef}}"

// servePrivacyRestore runs a handler behind PrivacyResponseRestore with the
// given placeholder->original mapping on the context.
func servePrivacyRestore(t *testing.T, replacements map[string]string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.GET("/test", func(c *gin.Context) {
		if replacements != nil {
			common.SetContextKey(c, constant.ContextKeyPrivacyReplacements, replacements)
		}
		c.Next()
	}, PrivacyResponseRestore(), handler)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
	return recorder
}

func TestPrivacyRestoreNonStreaming(t *testing.T) {
	recorder := servePrivacyRestore(t, map[string]string{privacyRestorePlaceholder: "sk-real-secret"}, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"text": "key " + privacyRestorePlaceholder})
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "key sk-real-secret")
	assert.NotContains(t, recorder.Body.String(), "{{WINGS_API_KEY_")
}

func TestPrivacyRestoreNoReplacements(t *testing.T) {
	recorder := servePrivacyRestore(t, nil, func(c *gin.Context) {
		c.String(http.StatusOK, "plain "+privacyRestorePlaceholder)
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "plain "+privacyRestorePlaceholder, recorder.Body.String())
}

func TestPrivacyRestoreKeepsStatus(t *testing.T) {
	recorder := servePrivacyRestore(t, map[string]string{privacyRestorePlaceholder: "restored"}, func(c *gin.Context) {
		c.Header("X-Test", "yes")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": privacyRestorePlaceholder})
	})
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.Equal(t, "yes", recorder.Header().Get("X-Test"))
	assert.Contains(t, recorder.Body.String(), "restored")
}

func TestPrivacyRestoreSSESplitPlaceholder(t *testing.T) {
	mapping := map[string]string{privacyRestorePlaceholder: "sk-real-secret"}
	recorder := servePrivacyRestore(t, mapping, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		parts := []string{
			"data: {\"text\":\"key {{WINGS_API",
			"_KEY_0123456789abcdef}} done\"}\n\n",
		}
		_, err := c.Writer.WriteString(parts[0])
		require.NoError(t, err)
		c.Writer.Flush()
		_, err = c.Writer.WriteString(parts[1])
		require.NoError(t, err)
		c.Writer.Flush()
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, "key sk-real-secret done")
	assert.NotContains(t, body, "{{WINGS_API")
}

func TestPrivacyRestoreLiteralBraces(t *testing.T) {
	recorder := servePrivacyRestore(t, map[string]string{privacyRestorePlaceholder: "restored"}, func(c *gin.Context) {
		_, err := c.Writer.WriteString("brace {{not-a-placeholder} ")
		require.NoError(t, err)
		c.Writer.Flush()
		_, err = c.Writer.WriteString("tail")
		require.NoError(t, err)
	})
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "brace {{not-a-placeholder} tail", recorder.Body.String())
}

func TestPrivacyRestoreIsolation(t *testing.T) {
	first := servePrivacyRestore(t, map[string]string{privacyRestorePlaceholder: "first-secret"}, func(c *gin.Context) {
		c.String(http.StatusOK, privacyRestorePlaceholder)
	})
	second := servePrivacyRestore(t, map[string]string{privacyRestorePlaceholder: "second-secret"}, func(c *gin.Context) {
		c.String(http.StatusOK, privacyRestorePlaceholder)
	})
	assert.Equal(t, "first-secret", first.Body.String())
	assert.Equal(t, "second-secret", second.Body.String())
}

func TestPrivacyRestoreEndToEndWithInbound(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.POST("/v1/chat/completions", PrivacyInbound(), func(c *gin.Context) {
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		body, err := storage.Bytes()
		require.NoError(t, err)
		// The handler (and upstream) only ever sees placeholders.
		assert.NotContains(t, string(body), privacyInboundTestKey)
		assert.Contains(t, string(body), "{{WINGS_API_KEY_")
		c.Data(http.StatusOK, "application/json", body)
	})
	requestBody := `{"messages":[{"role":"user","content":"key ` + privacyInboundTestKey + `"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = int64(len(requestBody))
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), privacyInboundTestKey, "client sees the real value again")
	assert.NotContains(t, recorder.Body.String(), "{{WINGS_API_KEY_")
}
