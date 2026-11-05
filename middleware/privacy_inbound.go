package middleware

import (
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/privacy"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// Inbound privacy filtering (issue #10): before a request reaches the relay
// pipeline, the textual fields of chat/responses/messages/gemini bodies are
// run through pkg/privacy and the body storage is replaced with the masked
// version, so every downstream consumer (handlers, logs built from the body,
// upstream channel requests) only ever sees placeholders. The placeholder map
// is stored on the context for the response writer to restore (issue #11).

type privacyInboundMode string

const (
	privacyModeOff    privacyInboundMode = "off"
	privacyModeWarn   privacyInboundMode = "warn"
	privacyModeRedact privacyInboundMode = "redact"
	privacyModeBlock  privacyInboundMode = "block"
)

type privacyInboundConfig struct {
	mode   privacyInboundMode
	engine *privacy.Engine
}

var (
	privacyInboundConfigOnce  sync.Once
	privacyInboundConfigValue *privacyInboundConfig
)

// resetPrivacyInboundConfigForTest makes the lazily loaded configuration
// reload on the next request (tests set the environment between cases).
func resetPrivacyInboundConfigForTest() {
	privacyInboundConfigOnce = sync.Once{}
	privacyInboundConfigValue = nil
}

func loadPrivacyInboundConfig() *privacyInboundConfig {
	mode := privacyInboundMode(strings.ToLower(strings.TrimSpace(os.Getenv("PRIVACY_INBOUND_MODE"))))
	switch mode {
	case privacyModeOff, privacyModeWarn, privacyModeRedact, privacyModeBlock:
	case "":
		mode = privacyModeOff
	default:
		common.SysLog(common.LogText("unknown PRIVACY_INBOUND_MODE %q, privacy filtering disabled", mode))
		mode = privacyModeOff
	}
	if mode == privacyModeOff {
		return &privacyInboundConfig{mode: mode}
	}
	policy := privacy.DefaultPolicy()
	if raw := strings.TrimSpace(os.Getenv("PRIVACY_POLICY_JSON")); raw != "" {
		var parsed privacy.Policy
		if err := common.Unmarshal([]byte(raw), &parsed); err != nil {
			common.SysLog(common.LogText("invalid PRIVACY_POLICY_JSON, using the default policy: %v", err))
		} else {
			policy = parsed
		}
	}
	engine, err := privacy.New(policy)
	if err != nil {
		common.SysLog(common.LogText("cannot build privacy engine, inbound filtering disabled: %v", err))
		return &privacyInboundConfig{mode: privacyModeOff}
	}
	return &privacyInboundConfig{mode: mode, engine: engine}
}

func getPrivacyInboundConfig() *privacyInboundConfig {
	privacyInboundConfigOnce.Do(func() {
		privacyInboundConfigValue = loadPrivacyInboundConfig()
	})
	return privacyInboundConfigValue
}

// privacyTextKeys are the JSON keys whose string values carry user/model
// conversations. Everything else (model, role, tools, image payloads, ids) is
// left byte-for-byte untouched.
var privacyTextKeys = map[string]struct{}{
	"content":      {},
	"text":         {},
	"input":        {},
	"prompt":       {},
	"system":       {},
	"instructions": {},
}

func isPrivacyTextKey(key string) bool {
	_, ok := privacyTextKeys[key]
	return ok
}

// privacyProtocolRoots restricts scanning to each protocol's textual roots
// (issue #36) instead of scanning every allowed key anywhere in the body.
func privacyProtocolRoots(path string) ([]string, bool) {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"), strings.HasPrefix(path, "/v1/completions"):
		return []string{"messages", "prompt"}, true
	case strings.HasPrefix(path, "/v1/responses"):
		return []string{"instructions", "input", "prompt"}, true
	case strings.HasPrefix(path, "/v1/messages"):
		return []string{"system", "messages"}, true
	case strings.HasPrefix(path, "/v1beta"):
		return []string{"systemInstruction", "contents"}, true
	default:
		return nil, false
	}
}

// privacySkipKeys are structured/continuation fields that must never be
// rewritten: changing them breaks reasoning signatures, tool schemas or binary
// payloads (AstrLink continuationPaths lesson).
var privacySkipKeys = map[string]struct{}{
	"signature":         {},
	"encrypted_content": {},
	"redacted_thinking": {},
	"thinking":          {},
	"metadata":          {},
	"image_url":         {},
	"data":              {},
	"file":              {},
	"audio":             {},
	"url":               {},
	"id":                {},
	"model":             {},
	"role":              {},
	"type":              {},
	"name":              {},
	"tool_call_id":      {},
	"tools":             {},
	"functions":         {},
	"tool_choice":       {},
	"format":            {},
	"mime_type":         {},
}

func isPrivacySkipKey(key string) bool {
	_, skip := privacySkipKeys[key]
	return skip
}

// privacyInboundFailClosed reports whether unparsable bodies must be rejected
// instead of forwarded (issue #36, default off).
func privacyInboundFailClosed() bool {
	return common.GetEnvOrDefaultBool("PRIVACY_INBOUND_FAIL_CLOSED", false)
}

// PrivacyInbound masks sensitive values in relay JSON bodies according to
// PRIVACY_INBOUND_MODE (off/warn/redact/block).
func PrivacyInbound() gin.HandlerFunc {
	return func(c *gin.Context) {
		// The relay entry also holds the abuse-guard concurrency slot and books
		// the request outcome for anomaly detection (issue #32). Keeping the
		// guard here avoids adding another handler to the relay chains.
		guardConfig := getAbuseGuardConfig()
		releaseAbuseSlot, abuseAllowed := acquireAbuseGuardSlot(c)
		if !abuseAllowed {
			return
		}
		defer func() {
			recordAbuseOutcome(c, guardConfig, c.Writer.Status())
			releaseAbuseSlot()
		}()
		config := getPrivacyInboundConfig()
		if config == nil || config.mode == privacyModeOff || config.engine == nil {
			c.Next()
			return
		}
		if c.Request == nil || c.Request.Method == http.MethodGet && c.Request.Body == nil {
			c.Next()
			return
		}
		if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
			c.Next()
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			logger.LogWarn(c, common.LogText("privacy: cannot read request body, passing it through: %v", err))
			c.Next()
			return
		}
		body, err := storage.Bytes()
		if err != nil || len(body) == 0 {
			c.Next()
			return
		}
		roots, hasRoots := privacyProtocolRoots(c.Request.URL.Path)
		collector := newPrivacyAuditCollector()
		redacted, replacements, findings, blocked, err := redactPrivacyJSONBody(body, config.engine, collector, roots, hasRoots)
		if err != nil {
			if privacyInboundFailClosed() {
				logger.LogWarn(c, common.LogText("privacy fail-closed: cannot parse request body: %v", err))
				abortWithOpenAiMessage(c, http.StatusServiceUnavailable, i18n.T(c, i18n.MsgPrivacyFailClosed), types.ErrorCodePrivacyFailClosed)
				return
			}
			logger.LogWarn(c, common.LogText("privacy: cannot parse request body, passing it through: %v", err))
			c.Next()
			return
		}
		if findings > 0 {
			event := model.PrivacyAuditEvent{
				UserId:       c.GetInt("id"),
				Username:     c.GetString("username"),
				RequestId:    c.GetString(common.RequestIdKey),
				Path:         c.Request.URL.Path,
				Mode:         string(config.mode),
				FindingCount: findings,
				CreatedAt:    common.GetTimestamp(),
			}
			auditFindings := collector.auditFindings()
			gopool.Go(func() {
				if err := model.RecordPrivacyAuditEvent(&event, auditFindings); err != nil {
					common.SysLog(common.LogText("failed to record privacy audit event (request=%s): %v", event.RequestId, err))
				}
			})
		}
		if config.mode == privacyModeWarn {
			if findings > 0 {
				logger.LogInfo(c, common.LogText("privacy dry-run: user=%d path=%s findings=%d", c.GetInt("id"), c.Request.URL.Path, findings))
			}
			c.Next()
			return
		}
		if blocked || (config.mode == privacyModeBlock && findings > 0) {
			logger.LogWarn(c, common.LogText("privacy: blocked request user=%d path=%s findings=%d", c.GetInt("id"), c.Request.URL.Path, findings))
			message := i18n.T(c, i18n.MsgPrivacyBlocked)
			abortWithOpenAiMessage(c, http.StatusForbidden, message, types.ErrorCodeSensitiveDataBlocked)
			return
		}
		if len(replacements) == 0 {
			c.Next()
			return
		}
		newStorage, err := common.CreateBodyStorage(redacted)
		if err != nil {
			if privacyInboundFailClosed() {
				logger.LogWarn(c, common.LogText("privacy fail-closed: cannot rebuild request body: %v", err))
				abortWithOpenAiMessage(c, http.StatusServiceUnavailable, i18n.T(c, i18n.MsgPrivacyFailClosed), types.ErrorCodePrivacyFailClosed)
				return
			}
			logger.LogWarn(c, common.LogText("privacy: cannot rebuild body storage, passing the original body through: %v", err))
			c.Next()
			return
		}
		c.Set(common.KeyBodyStorage, newStorage)
		c.Request.Body = io.NopCloser(newStorage)
		c.Request.ContentLength = int64(len(redacted))
		if c.Request.Header.Get("Content-Length") != "" {
			c.Request.Header.Set("Content-Length", strconv.Itoa(len(redacted)))
		}
		_ = storage.Close()
		common.SetContextKey(c, constant.ContextKeyPrivacyReplacements, replacements)
		finishRestore := installPrivacyRestoreWriter(c, replacements)
		defer finishRestore()
		logger.LogInfo(c, common.LogText("privacy: masked request user=%d path=%s findings=%d placeholders=%d", c.GetInt("id"), c.Request.URL.Path, findings, len(replacements)))
		c.Next()
	}
}

// privacyAuditSummary aggregates findings per rule so the audit table only
// stores rule ids, types, actions and counts — never the detected values.
type privacyAuditSummary struct {
	RuleID string
	Type   string
	Action privacy.Action
	Count  int
}

type privacyAuditCollector struct {
	order  []string
	counts map[string]*privacyAuditSummary
}

func newPrivacyAuditCollector() *privacyAuditCollector {
	return &privacyAuditCollector{counts: make(map[string]*privacyAuditSummary)}
}

func (c *privacyAuditCollector) add(findings []privacy.Finding) {
	for _, finding := range findings {
		key := finding.RuleID + "|" + finding.Type + "|" + string(finding.Action)
		summary, ok := c.counts[key]
		if !ok {
			summary = &privacyAuditSummary{RuleID: finding.RuleID, Type: finding.Type, Action: finding.Action}
			c.counts[key] = summary
			c.order = append(c.order, key)
		}
		summary.Count++
	}
}

func (c *privacyAuditCollector) auditFindings() []model.PrivacyAuditFinding {
	if c == nil || len(c.order) == 0 {
		return nil
	}
	findings := make([]model.PrivacyAuditFinding, 0, len(c.order))
	for _, key := range c.order {
		summary := c.counts[key]
		findings = append(findings, model.PrivacyAuditFinding{
			RuleId: summary.RuleID,
			Type:   summary.Type,
			Action: string(summary.Action),
			Count:  summary.Count,
		})
	}
	return findings
}

// redactPrivacyJSONBody walks the top-level object and rewrites the textual
// fields. Numbers, ids, tool declarations and binary payloads are preserved.
func redactPrivacyJSONBody(body []byte, engine *privacy.Engine, collector *privacyAuditCollector, roots []string, hasRoots bool) ([]byte, map[string]string, int, bool, error) {
	var root map[string]common.RawMessage
	if err := common.Unmarshal(body, &root); err != nil {
		return nil, nil, 0, false, err
	}
	replacements := make(map[string]string)
	findings := 0
	blocked := false
	changed := false
	for key, raw := range root {
		if hasRoots && !slices.Contains(roots, key) {
			continue
		}
		redacted, nodeChanged, nodeFindings, nodeBlocked := redactPrivacyValue(raw, isPrivacyTextKey(key), engine, collector, replacements)
		if nodeChanged {
			root[key] = redacted
			changed = true
		}
		findings += nodeFindings
		blocked = blocked || nodeBlocked
	}
	if !changed {
		return body, replacements, findings, blocked, nil
	}
	redactedBody, err := common.Marshal(root)
	if err != nil {
		return nil, nil, 0, false, err
	}
	return redactedBody, replacements, findings, blocked, nil
}

func redactPrivacyValue(raw common.RawMessage, allowText bool, engine *privacy.Engine, collector *privacyAuditCollector, replacements map[string]string) (common.RawMessage, bool, int, bool) {
	switch common.GetJsonType(raw) {
	case "string":
		if !allowText {
			return raw, false, 0, false
		}
		var value string
		if err := common.Unmarshal(raw, &value); err != nil {
			return raw, false, 0, false
		}
		// Binary payloads (data: URIs) are never text to redact.
		if strings.HasPrefix(value, "data:") {
			return raw, false, 0, false
		}
		result := engine.RedactText(value)
		collector.add(result.Findings)
		for placeholder, original := range result.Replacements {
			replacements[placeholder] = original
		}
		if result.Text == value {
			return raw, false, len(result.Findings), result.Blocked
		}
		redacted, err := common.Marshal(result.Text)
		if err != nil {
			return raw, false, len(result.Findings), result.Blocked
		}
		return redacted, true, len(result.Findings), result.Blocked
	case "array":
		var items []common.RawMessage
		if err := common.Unmarshal(raw, &items); err != nil {
			return raw, false, 0, false
		}
		findings := 0
		blocked := false
		changed := false
		for index, item := range items {
			// A string element of an allowed array (for example an embeddings
			// "input" list) inherits the array's allowance; objects and arrays
			// are decided by their own keys.
			itemAllowance := false
			if allowText && common.GetJsonType(item) == "string" {
				itemAllowance = true
			}
			redacted, itemChanged, itemFindings, itemBlocked := redactPrivacyValue(item, itemAllowance, engine, collector, replacements)
			if itemChanged {
				items[index] = redacted
				changed = true
			}
			findings += itemFindings
			blocked = blocked || itemBlocked
		}
		if !changed {
			return raw, false, findings, blocked
		}
		redacted, err := common.Marshal(items)
		if err != nil {
			return raw, false, findings, blocked
		}
		return redacted, true, findings, blocked
	case "object":
		var fields map[string]common.RawMessage
		if err := common.Unmarshal(raw, &fields); err != nil {
			return raw, false, 0, false
		}
		findings := 0
		blocked := false
		changed := false
		for key, field := range fields {
			if isPrivacySkipKey(key) {
				continue
			}
			redacted, fieldChanged, fieldFindings, fieldBlocked := redactPrivacyValue(field, isPrivacyTextKey(key), engine, collector, replacements)
			if fieldChanged {
				fields[key] = redacted
				changed = true
			}
			findings += fieldFindings
			blocked = blocked || fieldBlocked
		}
		if !changed {
			return raw, false, findings, blocked
		}
		redacted, err := common.Marshal(fields)
		if err != nil {
			return raw, false, findings, blocked
		}
		return redacted, true, findings, blocked
	default:
		return raw, false, 0, false
	}
}
