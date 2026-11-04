package privacy

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleAPIKey = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"
const sampleAWSKey = "AKIAIOSFODNN7EXAMPLE"
const sampleEmail = "li.wei@acme-corp.cn"
const samplePhone = "13800138000"

func newTestEngine(t *testing.T, mutate func(*Policy)) *Engine {
	t.Helper()
	policy := DefaultPolicy()
	if mutate != nil {
		mutate(&policy)
	}
	engine, err := New(policy)
	require.NoError(t, err)
	return engine
}

func TestRedactsSecrets(t *testing.T) {
	engine := newTestEngine(t, nil)
	text := "key=" + sampleAPIKey + " aws=" + sampleAWSKey + " jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlMTIzNDU2"

	result := engine.RedactText(text)
	assert.NotContains(t, result.Text, sampleAPIKey)
	assert.NotContains(t, result.Text, sampleAWSKey)
	assert.NotContains(t, result.Text, "eyJhbGciOiJIUzI1NiJ9")
	assert.Contains(t, result.Text, "{{WINGS_API_KEY_")
	assert.Contains(t, result.Text, "{{WINGS_JWT_")
	assert.Len(t, result.Replacements, 3)
	for _, finding := range result.Findings {
		assert.Equal(t, ActionRedact, finding.Action)
	}
}

func TestRedactsPrivateKey(t *testing.T) {
	engine := newTestEngine(t, nil)
	text := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234\n-----END RSA PRIVATE KEY-----"
	result := engine.RedactText(text)
	assert.NotContains(t, result.Text, "BEGIN RSA PRIVATE KEY")
	assert.Contains(t, result.Text, "{{WINGS_PRIVATE_KEY_")
}

func TestWarnsPersonalData(t *testing.T) {
	engine := newTestEngine(t, nil)
	text := "contact " + sampleEmail + " or " + samplePhone
	result := engine.RedactText(text)
	assert.Equal(t, text, result.Text, "warn findings must not rewrite the text")
	assert.Len(t, result.Findings, 2)
	for _, finding := range result.Findings {
		assert.Equal(t, ActionWarn, finding.Action)
	}
	assert.Empty(t, result.Replacements)
	assert.False(t, result.Blocked)
}

func TestBlockAction(t *testing.T) {
	engine := newTestEngine(t, func(policy *Policy) {
		for index := range policy.Rules {
			if policy.Rules[index].Type == "api_key" {
				policy.Rules[index].Action = ActionBlock
			}
		}
	})
	result := engine.RedactText("key=" + sampleAPIKey)
	assert.True(t, result.Blocked)
	assert.Contains(t, result.Text, "{{WINGS_API_KEY_", "block still masks the value")
}

func TestCustomRegexValueGroup(t *testing.T) {
	engine := newTestEngine(t, func(policy *Policy) {
		policy.Rules = append(policy.Rules, Rule{
			ID:      "project-code",
			Type:    "custom",
			Pattern: `project-(?P<value>[A-Z0-9]{6})`,
			Action:  ActionRedact,
		})
	})
	result := engine.RedactText("deploy project-AB12CD now")
	assert.True(t, strings.HasPrefix(result.Text, "deploy project-{{WINGS_CUSTOM_"))
	assert.NotContains(t, result.Text, "AB12CD")
	require.Len(t, result.Findings, 1)
	assert.Equal(t, "AB12CD", result.Findings[0].Value)
}

func TestCustomWords(t *testing.T) {
	engine := newTestEngine(t, func(policy *Policy) {
		policy.Words = []string{"Nightjar"}
		policy.WordsIgnoreCase = true
	})
	result := engine.RedactText("NIGHTJAR and nightjar and Nightjar")
	assert.NotContains(t, result.Text, "NIGHTJAR")
	assert.NotContains(t, result.Text, "nightjar")
	assert.Contains(t, result.Text, "{{WINGS_WORD_")
}

func TestWhitelist(t *testing.T) {
	engine := newTestEngine(t, func(policy *Policy) {
		policy.Whitelist = []string{sampleAPIKey}
		policy.WhitelistPatterns = []string{`^sk-test-`}
	})
	result := engine.RedactText(sampleAPIKey + " " + "sk-test-abcdefghijklmnopqrstuvwxyz")
	assert.Equal(t, sampleAPIKey+" sk-test-abcdefghijklmnopqrstuvwxyz", result.Text)
	assert.Empty(t, result.Findings)
}

func TestPlaceholderDeterministic(t *testing.T) {
	first := newTestEngine(t, nil).RedactText("key=" + sampleAPIKey)
	second := newTestEngine(t, nil).RedactText("key=" + sampleAPIKey)
	assert.Equal(t, first.Text, second.Text, "same value must yield the same placeholder")
	other := newTestEngine(t, nil).RedactText("key=" + sampleAWSKey)
	assert.NotEqual(t, first.Text, other.Text)

	placeholder := first.Findings[0].Placeholder
	require.NotEmpty(t, placeholder)
	require.True(t, regexp.MustCompile(`^\{\{WINGS_[A-Z_]+_[0-9a-f]{16}\}\}$`).MatchString(placeholder))
}

func TestRestore(t *testing.T) {
	engine := newTestEngine(t, nil)
	text := "key=" + sampleAPIKey
	result := engine.RedactText(text)
	restored := Restore(result.Text, result.Replacements)
	assert.Equal(t, text, restored)
}

func TestPolicyValidation(t *testing.T) {
	_, err := New(Policy{Enabled: true, Rules: []Rule{{ID: "bad", Type: "custom", Pattern: "(", Action: ActionRedact}}})
	assert.Error(t, err)

	many := make([]Rule, 0, maxCustomRules+1)
	for index := 0; index <= maxCustomRules; index++ {
		many = append(many, Rule{ID: "r", Type: "custom", Pattern: "abc", Action: ActionRedact})
	}
	_, err = New(Policy{Enabled: true, Rules: many})
	assert.Error(t, err)
}

func TestDisabled(t *testing.T) {
	engine := newTestEngine(t, func(policy *Policy) { policy.Enabled = false })
	result := engine.RedactText("key=" + sampleAPIKey)
	assert.Equal(t, "key="+sampleAPIKey, result.Text)
	assert.Empty(t, result.Findings)
}

func TestRedactsMixedText(t *testing.T) {
	engine := newTestEngine(t, nil)
	text := "请把 key=" + sampleAPIKey + " 发到 " + sampleEmail + "，手机号 " + samplePhone
	result := engine.RedactText(text)
	assert.NotContains(t, result.Text, sampleAPIKey)
	assert.Contains(t, result.Text, sampleEmail, "personal data is only warned by default")
	assert.Contains(t, result.Text, samplePhone)
}

func BenchmarkRedact1KB(b *testing.B) {
	text := strings.Repeat("some normal log line with email a@b.com and words, ", 20) + sampleAPIKey
	benchmarkRedact(b, text)
}

func BenchmarkRedact10KB(b *testing.B) {
	text := strings.Repeat("some normal log line with email a@b.com and words, ", 200) + sampleAPIKey
	benchmarkRedact(b, text)
}

func benchmarkRedact(b *testing.B, text string) {
	b.Helper()
	engine, err := New(DefaultPolicy())
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for range b.N {
		_ = engine.RedactText(text)
	}
}
