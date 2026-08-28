package archive

import (
	"regexp"
	"strings"
	"unicode"
)

// DetectMessagePolicy deterministically classifies exact message content into
// fail-closed policy flags. The flags gate two defenses: mutation proposals
// citing a flagged message stage for review (EVIDENCE_REQUIRES_REVIEW), and
// retrieval excludes flagged memories from automatic context. False positives
// are fail-safe (staged, excluded); the patterns are deliberately conservative
// and statement-oriented so that questions such as "what is a password?" are
// not flagged.
func DetectMessagePolicy(content string) (secretLike, instructionLike bool) {
	lower := strings.ToLower(content)
	for _, pattern := range instructionLikePatterns {
		if strings.Contains(lower, pattern) {
			instructionLike = true
			break
		}
	}
	for _, pattern := range secretLikePatterns {
		if strings.Contains(lower, pattern) {
			secretLike = true
			break
		}
	}
	if !secretLike {
		secretLike = structuredSecretPattern.MatchString(content) ||
			containsLuhnCard(content) || containsLabeledCredential(content)
	}
	return secretLike, instructionLike
}

var structuredSecretPattern = regexp.MustCompile(`(?i)(?:-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b|\b(?:AKIA|ASIA)[A-Z0-9]{16}\b|\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b|\bsk-[A-Za-z0-9_-]{20,}\b|\bauthorization\s*:\s*bearer\s+[^\s]+|\bbearer\s+[A-Za-z0-9._~+/=-]{16,}|\b(?:https?|postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s/:@]+:[^\s/@]+@[^\s]+|\b(?:API_KEY|API_TOKEN|ACCESS_TOKEN|SECRET_KEY|CLIENT_SECRET|DATABASE_URL|DB_PASSWORD|PASSWORD)\s*=\s*[^\s"']{8,})`)

var credentialAssignmentPattern = regexp.MustCompile(`(?i)(?:password|passphrase|api[ _-]?key|secret(?:[ _-]?key)?|access[ _-]?token|client[ _-]?secret|密码|口令|密钥|令牌|凭证|数据库密码)\s*(?:is|是|为|:|：|=)\s*["']?([^\s"']{8,})`)

func containsLabeledCredential(content string) bool {
	for _, match := range credentialAssignmentPattern.FindAllStringSubmatch(content, -1) {
		value := match[1]
		var lower, upper, digit, symbol bool
		for _, r := range value {
			switch {
			case unicode.IsLower(r):
				lower = true
			case unicode.IsUpper(r):
				upper = true
			case unicode.IsDigit(r):
				digit = true
			default:
				symbol = true
			}
		}
		classes := 0
		for _, present := range []bool{lower, upper, digit, symbol} {
			if present {
				classes++
			}
		}
		if classes >= 2 {
			return true
		}
	}
	return false
}

func containsLuhnCard(content string) bool {
	for _, candidate := range regexp.MustCompile(`(?:\d[ -]?){13,19}`).FindAllString(content, -1) {
		digits := make([]int, 0, 19)
		for _, r := range candidate {
			if unicode.IsDigit(r) && r <= '9' {
				digits = append(digits, int(r-'0'))
			}
		}
		if len(digits) < 13 || len(digits) > 19 {
			continue
		}
		sum, double := 0, false
		for i := len(digits) - 1; i >= 0; i-- {
			value := digits[i]
			if double {
				value *= 2
				if value > 9 {
					value -= 9
				}
			}
			sum += value
			double = !double
		}
		if sum%10 == 0 {
			return true
		}
	}
	return false
}

// instructionLikePatterns match content that attempts to override or replace
// the agent's governing instructions. The patterns are assertion forms only, so
// questions about the concepts ("what is a system prompt?") are not flagged.
// CJK patterns match directly.
var instructionLikePatterns = []string{
	"ignore all previous instructions",
	"ignore previous instructions",
	"ignore your instructions",
	"ignore everything before",
	"ignore the system prompt",
	"disregard all previous",
	"disregard your instructions",
	"disregard the system prompt",
	"override your instructions",
	"override the system prompt",
	"your new instructions",
	"your new system prompt",
	"from now on you are",
	"you are now a",
	"jailbreak",
	"forget all previous instructions",
	"忽略所有之前的指令",
	"忽略之前的指令",
	"无视之前的指令",
	"无视你的指令",
	"忘记所有之前的指令",
	"从此刻起你是",
	"从现在开始你是",
	"你的新指令是",
	"你的新系统提示词",
	"系统提示词是",
	"越狱",
}

// secretLikePatterns match statements of credentials or secrets, not questions
// or mentions of the words themselves.
var secretLikePatterns = []string{
	"my password is",
	"my password:",
	"the password is",
	"my api key",
	"my api-key",
	"api key is",
	"api-key is",
	"apikey is",
	"my secret key",
	"secret key is",
	"access key is",
	"private key is",
	"my token is",
	"credentials are",
	"credit card number",
	"card number is",
	"ssn is",
	"passport number is",
	"我的密码是",
	"密码是",
	"我的密钥是",
	"密钥是",
	"api 密钥",
	"账号密码",
	"银行卡号",
	"身份证号",
	"信用卡号",
}
