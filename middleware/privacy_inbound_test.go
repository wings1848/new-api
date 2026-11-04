package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const privacyInboundTestKey = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"

var privacyInboundTestInit sync.Once

func setupPrivacyInboundTest(t *testing.T, mode, policyJSON string) {
	t.Helper()
	privacyInboundTestInit.Do(func() {
		require.NoError(t, i18n.Init())
	})
	t.Setenv("PRIVACY_INBOUND_MODE", mode)
	if policyJSON == "" {
		t.Setenv("PRIVACY_POLICY_JSON", "")
	} else {
		t.Setenv("PRIVACY_POLICY_JSON", policyJSON)
	}
	resetPrivacyInboundConfigForTest()
	t.Cleanup(resetPrivacyInboundConfigForTest)
}

// runPrivacyInbound drives the middleware and returns what the downstream
// handler saw (re-marshalled body), the replacements it could read from the
// context, and whether the downstream handler ran at all.
func runPrivacyInbound(t *testing.T, path, contentType, body string) (*httptest.ResponseRecorder, string, map[string]string, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	var seen string
	var replacements map[string]string
	nextCalled := false
	router := gin.New()
	router.POST(path, PrivacyInbound(), func(c *gin.Context) {
		nextCalled = true
		storage, storageErr := common.GetBodyStorage(c)
		require.NoError(t, storageErr)
		raw, readErr := storage.Bytes()
		require.NoError(t, readErr)
		seen = string(raw)
		if value, ok := common.GetContextKey(c, constant.ContextKeyPrivacyReplacements); ok {
			replacements, _ = value.(map[string]string)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", contentType)
	request.ContentLength = int64(len(body))
	router.ServeHTTP(recorder, request)
	return recorder, seen, replacements, nextCalled
}

func TestPrivacyInboundRedactsChat(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"my key is ` + privacyInboundTestKey + ` please use it"}],"tools":[{"type":"function","function":{"name":"x","arguments":{"key":"` + privacyInboundTestKey + `"}}}]}`
	recorder, seen, replacements, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.True(t, nextCalled)
	// Conversation text is masked; tool declarations/arguments are left as they
	// are (issue #9 decision: scanning tool schemas would alter what the model
	// is told about its tools).
	assert.Contains(t, seen, "{{WINGS_API_KEY_")
	assert.Contains(t, strings.ReplaceAll(seen, "{{WINGS_API_KEY_53fc4f225c194ebb}}", ""), privacyInboundTestKey, "tool arguments are not rewritten")
	require.NotEmpty(t, replacements)
}

func TestPrivacyInboundWarnKeepsBody(t *testing.T) {
	setupPrivacyInboundTest(t, "warn", "")
	body := `{"messages":[{"role":"user","content":"my key is ` + privacyInboundTestKey + `"}]}`
	recorder, seen, replacements, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.Contains(t, seen, privacyInboundTestKey)
	assert.Empty(t, replacements)
}

func TestPrivacyInboundBlock(t *testing.T) {
	setupPrivacyInboundTest(t, "block", "")
	body := `{"messages":[{"role":"user","content":"my key is ` + privacyInboundTestKey + `"}]}`
	recorder, _, _, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.False(t, nextCalled)
	assert.Contains(t, recorder.Body.String(), "sensitive_data_blocked")
}

func TestPrivacyInboundOff(t *testing.T) {
	setupPrivacyInboundTest(t, "off", "")
	body := `{"messages":[{"role":"user","content":"my key is ` + privacyInboundTestKey + `"}]}`
	recorder, seen, replacements, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.Contains(t, seen, privacyInboundTestKey)
	assert.Empty(t, replacements)
}

func TestPrivacyInboundNonJSON(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	recorder, seen, _, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "text/plain", privacyInboundTestKey)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.Contains(t, seen, privacyInboundTestKey)
}

func TestPrivacyInboundResponsesAndGemini(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	responsesBody := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"key ` + privacyInboundTestKey + `"}]}]}`
	recorder, seen, _, nextCalled := runPrivacyInbound(t, "/v1/responses", "application/json", responsesBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.Contains(t, seen, "{{WINGS_API_KEY_")

	geminiBody := `{"contents":[{"role":"user","parts":[{"text":"key ` + privacyInboundTestKey + `"}]}],"systemInstruction":{"parts":[{"text":"system ` + privacyInboundTestKey + `"}]}}`
	recorder, seen, _, nextCalled = runPrivacyInbound(t, "/v1beta/models/gemini-pro:generateContent", "application/json", geminiBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.NotContains(t, seen, privacyInboundTestKey)
	assert.Contains(t, seen, "{{WINGS_API_KEY_")
}

func TestPrivacyInboundSkipsBinary(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example.com/a.png?token=` + privacyInboundTestKey + `"}},{"type":"text","text":"data:image/png;base64,` + privacyInboundTestKey + `"}]}]}`
	recorder, seen, _, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.Contains(t, seen, "https://example.com/a.png?token="+privacyInboundTestKey)
	assert.Contains(t, seen, "data:image/png;base64,"+privacyInboundTestKey)
}

func TestPrivacyInboundContextReplacements(t *testing.T) {
	setupPrivacyInboundTest(t, "redact", "")
	body := `{"messages":[{"role":"user","content":"key ` + privacyInboundTestKey + `"}]}`
	_, firstSeen, firstReplacements, _ := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	_, secondSeen, secondReplacements, _ := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.NotEmpty(t, firstReplacements)
	require.NotEmpty(t, secondReplacements)
	assert.Equal(t, firstSeen, secondSeen, "the same value must produce the same placeholder")
	for placeholder := range firstReplacements {
		assert.Contains(t, secondSeen, placeholder)
		assert.NotContains(t, placeholder, privacyInboundTestKey)
	}
}

func TestPrivacyInboundPolicyOverride(t *testing.T) {
	policy := `{"enabled":true,"rules":[{"id":"email","type":"email","pattern":"[A-Za-z0-9._%+\\-]+@[A-Za-z0-9.\\-]+\\.[A-Za-z]{2,}","action":"redact","builtin":true}]}`
	setupPrivacyInboundTest(t, "redact", policy)
	body := `{"messages":[{"role":"user","content":"mail me at li.wei@acme-corp.cn"}]}`
	recorder, seen, _, nextCalled := runPrivacyInbound(t, "/v1/chat/completions", "application/json", body)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, nextCalled)
	assert.NotContains(t, seen, "li.wei@acme-corp.cn")
	assert.Contains(t, seen, "{{WINGS_EMAIL_")
}

var _ = strings.Contains
