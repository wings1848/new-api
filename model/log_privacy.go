package model

import (
	"os"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/privacy"
)

// Log and error-message masking (issue #12): every row that reaches the log
// database (SQL or ClickHouse) and the audit log is sanitized first, and error
// messages logged to the console by the log writers are sanitized too. The
// engine is shared with the inbound/outbound privacy filter via
// PRIVACY_POLICY_JSON; PRIVACY_LOG_MODE (off/warn/redact, default off) decides
// whether log rows are rewritten.

type logPrivacySettings struct {
	engine   *privacy.Engine
	warnOnly bool
}

var (
	logPrivacyOnce  sync.Once
	logPrivacyValue *logPrivacySettings
)

// resetLogPrivacyEngineForTest makes the lazily loaded settings reload on the
// next call (tests change the environment between cases).
func resetLogPrivacyEngineForTest() {
	logPrivacyOnce = sync.Once{}
	logPrivacyValue = nil
}

func loadLogPrivacySettings() *logPrivacySettings {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("PRIVACY_LOG_MODE")))
	switch mode {
	case "", "off":
		return nil
	case "warn", "redact":
	default:
		common.SysLog(common.LogText("unknown PRIVACY_LOG_MODE %q, log privacy filtering disabled", mode))
		return nil
	}
	policy := privacy.DefaultPolicy()
	if raw := strings.TrimSpace(os.Getenv("PRIVACY_POLICY_JSON")); raw != "" {
		var parsed privacy.Policy
		if err := common.Unmarshal([]byte(raw), &parsed); err != nil {
			common.SysLog(common.LogText("invalid PRIVACY_POLICY_JSON for logs, using the default policy: %v", err))
		} else {
			policy = parsed
		}
	}
	engine, err := privacy.New(policy)
	if err != nil {
		common.SysLog(common.LogText("cannot build privacy engine for logs, log masking disabled: %v", err))
		return nil
	}
	return &logPrivacySettings{engine: engine, warnOnly: mode == "warn"}
}

func getLogPrivacySettings() *logPrivacySettings {
	logPrivacyOnce.Do(func() {
		logPrivacyValue = loadLogPrivacySettings()
	})
	return logPrivacyValue
}

// sanitizeLogText masks sensitive values in one log string. It returns the
// input unchanged when log masking is off or only in warn mode.
func sanitizeLogText(text string) string {
	if text == "" {
		return text
	}
	settings := getLogPrivacySettings()
	if settings == nil || settings.engine == nil {
		return text
	}
	result := settings.engine.RedactText(text)
	if settings.warnOnly {
		return text
	}
	return result.Text
}

// logPrivacyRedactionEnabled reports whether sanitizeLogText actually rewrites.
func logPrivacyRedactionEnabled() bool {
	settings := getLogPrivacySettings()
	return settings != nil && settings.engine != nil && !settings.warnOnly
}
