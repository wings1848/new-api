package privacy

import (
	"strings"
)

// DefaultPolicy enables the built-in rule pack. Secrets are redacted by
// default (high confidence, low risk of breaking agent prompts); personal data
// is only warned about until an administrator opts into redaction.
func DefaultPolicy() Policy {
	return Policy{
		Enabled: true,
		Rules: []Rule{
			{
				ID:      "private-key",
				Type:    "private_key",
				Pattern: `-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`,
				Action:  ActionRedact,
				Builtin: true,
			},
			{
				ID:   "api-key",
				Type: "api_key",
				Pattern: `(?:sk-proj-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{20,}|` +
					`ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|` +
					`AIza[0-9A-Za-z_-]{30,}|(?:AKIA|ASIA)[0-9A-Z]{16}|hf_[A-Za-z0-9]{20,}|` +
					`gsk_[A-Za-z0-9]{20,}|xai-[A-Za-z0-9]{20,}|npm_[A-Za-z0-9]{20,}|` +
					`pypi-AgE[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|dop_v1_[a-f0-9]{32,}|` +
					`SG\.[A-Za-z0-9_-]{20,})`,
				Action:  ActionRedact,
				Builtin: true,
			},
			{
				ID:      "jwt",
				Type:    "jwt",
				Pattern: `eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`,
				Action:  ActionRedact,
				Builtin: true,
			},
			{
				ID:      "email",
				Type:    "email",
				Pattern: `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`,
				Action:  ActionWarn,
				Builtin: true,
			},
			{
				ID:      "phone-cn",
				Type:    "phone_cn",
				Pattern: `(?P<value>(?:\+?86[\s-]?)?1[3-9]\d{9})`,
				Action:  ActionWarn,
				Builtin: true,
			},
			{
				ID:      "id-card-cn",
				Type:    "id_card_cn",
				Pattern: `(?P<value>[1-9]\d{16}[\dXx])`,
				Action:  ActionWarn,
				Builtin: true,
			},
			{
				ID:      "bank-card",
				Type:    "bank_card",
				Pattern: `(?P<value>(?:\d[ -]?){12,18}\d)`,
				Action:  ActionWarn,
				Builtin: true,
			},
		},
	}
}

func digitsOnly(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, char := range value {
		if char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func validBankCard(value string) bool {
	digits := digitsOnly(value)
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	return luhnValid(digits)
}

func luhnValid(digits string) bool {
	sum := 0
	double := false
	for index := len(digits) - 1; index >= 0; index-- {
		digit := int(digits[index] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

func validChineseID(value string) bool {
	if len(value) != 18 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checksum := []byte("10X98765432")
	sum := 0
	for index := 0; index < 17; index++ {
		char := value[index]
		if char < '0' || char > '9' {
			return false
		}
		sum += int(char-'0') * weights[index]
	}
	last := value[17]
	if last == 'x' {
		last = 'X'
	}
	return checksum[sum%11] == last
}

func validChinesePhone(value string) bool {
	digits := digitsOnly(value)
	if strings.HasPrefix(digits, "86") && len(digits) == 13 {
		digits = digits[2:]
	}
	if len(digits) != 11 || digits[0] != '1' || digits[1] < '3' || digits[1] > '9' {
		return false
	}
	first := digits[0]
	allSame := true
	for index := 1; index < len(digits); index++ {
		if digits[index] != first {
			allSame = false
			break
		}
	}
	return !allSame
}

// builtinPrefilter cheaply rejects a built-in rule when the text cannot
// contain its kind. Custom rules have no prefilter and always run.
func builtinPrefilter(ruleID string) func(string) bool {
	switch ruleID {
	case "private-key":
		return func(text string) bool { return strings.Contains(text, "PRIVATE KEY") }
	case "api-key":
		return func(text string) bool {
			return containsAny(text,
				"sk-", "ghp_", "github_pat_", "glpat-", "AIza", "AKIA", "ASIA",
				"hf_", "gsk_", "xai-", "npm_", "pypi-", "xox", "dop_v1_", "SG.")
		}
	case "jwt":
		return func(text string) bool { return strings.Contains(text, "eyJ") }
	case "email":
		return func(text string) bool { return strings.Contains(text, "@") }
	case "phone-cn":
		return func(text string) bool { return hasDigitRun(text, 11) }
	case "id-card-cn":
		return func(text string) bool { return hasDigitRun(text, 17) }
	case "bank-card":
		return mayContainBankCard
	default:
		return nil
	}
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// hasDigitRun reports whether text has at least length consecutive digits.
func hasDigitRun(text string, length int) bool {
	run := 0
	for index := 0; index < len(text); index++ {
		if text[index] >= '0' && text[index] <= '9' {
			run++
			if run >= length {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// mayContainBankCard cheaply detects a 13-19 digit card number, allowing the
// spaces and dashes a formatted card carries between digit groups.
func mayContainBankCard(text string) bool {
	digits := 0
	onlyCardChars := true
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch {
		case char >= '0' && char <= '9':
			digits++
			if digits >= 13 {
				return true
			}
			onlyCardChars = true
		case (char == ' ' || char == '-') && onlyCardChars && digits > 0:
			// separator inside a possible card number
		default:
			digits = 0
			onlyCardChars = char == ' ' || char == '-'
		}
	}
	return false
}
