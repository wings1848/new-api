// Package privacy detects secrets and personal data in gateway traffic and
// replaces them with deterministic placeholders before they reach an upstream
// provider (issue #9). It is deliberately small and dependency-free: the relay
// integration, streaming restore and audit pages are separate issues.
package privacy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	maxCustomRules  = 64
	maxPatternRunes = 512
)

// Action is what the engine does with a detected value.
type Action string

const (
	// ActionWarn only reports the value; the text is left untouched.
	ActionWarn Action = "warn"
	// ActionBlock masks the value and marks the result blocked.
	ActionBlock Action = "block"
	// ActionRedact replaces the value with a placeholder.
	ActionRedact Action = "redact"
)

// Rule describes one detection rule. Builtin rules ship with DefaultPolicy;
// custom rules are appended by an administrator (Pattern at most
// maxPatternRunes, at most maxCustomRules custom rules in one policy).
//
// A custom pattern may expose a (?P<value>...) group to replace only that
// part of the match (for example `project-(?P<value>[A-Z0-9]{6})`).
type Rule struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
	Action  Action `json:"action"`
	// Disabled turns a rule off; the zero value keeps it enabled.
	Disabled bool `json:"disabled,omitempty"`
	// Builtin marks the rules shipped with the engine.
	Builtin bool `json:"builtin,omitempty"`
}

// Policy is the detection configuration. It is JSON-serializable so an admin
// API/UI can manage it later (issue #13).
type Policy struct {
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules,omitempty"`
	// Words are literal values (names, codenames, hosts) always masked.
	Words []string `json:"words,omitempty"`
	// WordsIgnoreCase matches Words regardless of case.
	WordsIgnoreCase bool `json:"words_ignore_case,omitempty"`
	// Whitelist holds values that are never masked; WhitelistPatterns match
	// whole detected values by regular expression.
	Whitelist         []string `json:"whitelist,omitempty"`
	WhitelistPatterns []string `json:"whitelist_patterns,omitempty"`
}

// Finding is one detected value.
type Finding struct {
	RuleID      string `json:"rule_id"`
	Type        string `json:"type"`
	Action      Action `json:"action"`
	Value       string `json:"-"`
	Start       int    `json:"-"`
	End         int    `json:"-"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Result is one detection pass over a text.
type Result struct {
	Text         string
	Findings     []Finding
	Replacements map[string]string
	Blocked      bool
}

// Engine is an immutable, concurrency-safe detector built from a Policy.
type Engine struct {
	enabled           bool
	rules             []compiledRule
	words             []compiledWord
	whitelist         map[string]struct{}
	whitelistPatterns []*regexp.Regexp
	key               []byte
}

type compiledRule struct {
	rule            Rule
	pattern         *regexp.Regexp
	valueGroupIndex int
	validate        func(string) bool
	// prefilter cheaply rejects a rule when the text cannot contain its kind;
	// it keeps the common no-secret request off most regular expressions.
	prefilter func(string) bool
	order     int
}

type compiledWord struct {
	value   string
	pattern *regexp.Regexp
}

type rawMatch struct {
	start int
	end   int
	value string
	rule  compiledRule
	order int
}

// New compiles a policy. Invalid patterns, oversized patterns and too many
// custom rules fail here, at startup, rather than on the request path.
func New(policy Policy) (*Engine, error) {
	engine := &Engine{enabled: policy.Enabled, whitelist: make(map[string]struct{}), key: placeholderKey()}
	if !engine.enabled {
		return engine, nil
	}
	for _, value := range policy.Whitelist {
		if value = strings.TrimSpace(value); value != "" {
			engine.whitelist[value] = struct{}{}
		}
	}
	for _, pattern := range policy.WhitelistPatterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid whitelist pattern %q: %w", pattern, err)
		}
		engine.whitelistPatterns = append(engine.whitelistPatterns, compiled)
	}
	customRules := 0
	for index, rule := range policy.Rules {
		if rule.Disabled {
			continue
		}
		if !rule.Builtin {
			customRules++
			if len([]rune(rule.Pattern)) > maxPatternRunes {
				return nil, fmt.Errorf("custom rule %q pattern exceeds %d runes", rule.ID, maxPatternRunes)
			}
		}
		compiled, err := compileRule(rule, index)
		if err != nil {
			return nil, err
		}
		engine.rules = append(engine.rules, compiled)
	}
	if customRules > maxCustomRules {
		return nil, fmt.Errorf("too many custom rules: %d (max %d)", customRules, maxCustomRules)
	}
	words := policy.Words
	if len(words) > maxCustomRules {
		words = words[:maxCustomRules]
	}
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		pattern := regexp.QuoteMeta(word)
		if policy.WordsIgnoreCase {
			pattern = "(?i)" + pattern
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid word %q: %w", word, err)
		}
		engine.words = append(engine.words, compiledWord{value: word, pattern: compiled})
	}
	return engine, nil
}

func compileRule(rule Rule, order int) (compiledRule, error) {
	pattern, err := regexp.Compile(rule.Pattern)
	if err != nil {
		return compiledRule{}, fmt.Errorf("invalid rule %q pattern: %w", rule.ID, err)
	}
	compiled := compiledRule{
		rule:            rule,
		pattern:         pattern,
		valueGroupIndex: pattern.SubexpIndex("value"),
		order:           order,
	}
	switch rule.Type {
	case "bank_card":
		compiled.validate = validBankCard
	case "id_card_cn":
		compiled.validate = validChineseID
	case "phone_cn":
		compiled.validate = validChinesePhone
	}
	if rule.Builtin {
		compiled.prefilter = builtinPrefilter(rule.ID)
	}
	if rule.Action != ActionWarn && rule.Action != ActionBlock && rule.Action != ActionRedact {
		compiled.rule.Action = ActionRedact
	}
	return compiled, nil
}

// RedactText detects every configured value and rewrites redacted/blocked ones
// with placeholders. Warn-only values are reported without rewriting the text.
func (e *Engine) RedactText(text string) Result {
	result := Result{Text: text}
	if e == nil || !e.enabled || text == "" {
		return result
	}
	matches := e.detect(text)
	if len(matches) == 0 {
		return result
	}
	replacements := make(map[string]string, len(matches))
	var builder strings.Builder
	builder.Grow(len(text) + 16*len(matches))
	last := 0
	for _, match := range matches {
		if match.start < last {
			continue
		}
		builder.WriteString(text[last:match.start])
		finding := Finding{
			RuleID: match.rule.rule.ID,
			Type:   normalizeType(match.rule.rule.Type),
			Action: match.rule.rule.Action,
			Value:  match.value,
			Start:  match.start,
			End:    match.end,
		}
		switch finding.Action {
		case ActionWarn:
			builder.WriteString(text[match.start:match.end])
		case ActionBlock:
			result.Blocked = true
			finding.Placeholder = e.placeholderFor(finding.Type, match.value)
			builder.WriteString(finding.Placeholder)
			replacements[finding.Placeholder] = match.value
		default:
			finding.Placeholder = e.placeholderFor(finding.Type, match.value)
			builder.WriteString(finding.Placeholder)
			replacements[finding.Placeholder] = match.value
		}
		result.Findings = append(result.Findings, finding)
		last = match.end
	}
	builder.WriteString(text[last:])
	result.Text = builder.String()
	if len(replacements) > 0 {
		result.Replacements = replacements
	}
	return result
}

func (e *Engine) detect(text string) []rawMatch {
	matches := make([]rawMatch, 0)
	for _, rule := range e.rules {
		if rule.prefilter != nil && !rule.prefilter(text) {
			continue
		}
		for _, indexes := range rule.pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := indexes[0], indexes[1]
			if rule.valueGroupIndex >= 0 {
				group := rule.valueGroupIndex * 2
				if group+1 >= len(indexes) || indexes[group] < 0 {
					continue
				}
				start, end = indexes[group], indexes[group+1]
			}
			if start < 0 || end <= start {
				continue
			}
			value := text[start:end]
			if rule.validate != nil && !rule.validate(value) {
				continue
			}
			if e.isWhitelisted(value) {
				continue
			}
			matches = append(matches, rawMatch{start: start, end: end, value: value, rule: rule})
		}
	}
	for _, word := range e.words {
		for _, indexes := range word.pattern.FindAllStringIndex(text, -1) {
			value := text[indexes[0]:indexes[1]]
			if e.isWhitelisted(value) {
				continue
			}
			matches = append(matches, rawMatch{
				start: indexes[0],
				end:   indexes[1],
				value: value,
				rule: compiledRule{
					rule:  Rule{ID: "custom-word", Type: "word", Action: ActionRedact},
					order: len(e.rules),
				},
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		if matches[i].end != matches[j].end {
			return matches[i].end > matches[j].end
		}
		return matches[i].rule.order < matches[j].rule.order
	})
	return matches
}

func (e *Engine) isWhitelisted(value string) bool {
	if len(e.whitelist) == 0 && len(e.whitelistPatterns) == 0 {
		return false
	}
	if _, ok := e.whitelist[value]; ok {
		return true
	}
	for entry := range e.whitelist {
		if strings.Contains(entry, value) {
			return true
		}
	}
	for _, pattern := range e.whitelistPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (e *Engine) placeholderFor(kind string, value string) string {
	mac := hmac.New(sha256.New, e.key)
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	digest := hex.EncodeToString(mac.Sum(nil)[:8])
	return "{{WINGS_" + kind + "_" + digest + "}}"
}

func normalizeType(kind string) string {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind == "" {
		return "SECRET"
	}
	var builder strings.Builder
	for _, char := range kind {
		if (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

var placeholderKeyOnce = sync.OnceValue(func() []byte {
	if fromEnv := os.Getenv("PRIVACY_PLACEHOLDER_KEY"); fromEnv != "" {
		return []byte(fromEnv)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("privacy: cannot generate placeholder key: %v", err))
	}
	return key
})

func placeholderKey() []byte {
	return placeholderKeyOnce()
}

// Restore replaces placeholders in text with their original values.
func Restore(text string, replacements map[string]string) string {
	if len(replacements) == 0 || text == "" {
		return text
	}
	placeholders := make([]string, 0, len(replacements))
	for placeholder := range replacements {
		placeholders = append(placeholders, placeholder)
	}
	sort.Slice(placeholders, func(i, j int) bool { return len(placeholders[i]) > len(placeholders[j]) })
	for _, placeholder := range placeholders {
		text = strings.ReplaceAll(text, placeholder, replacements[placeholder])
	}
	return text
}
