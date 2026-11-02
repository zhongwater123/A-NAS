// Package textread turns what the text reader read from a photo into a
// document type and checked fields (docs/architecture/photo-ocr.md#读取判型与核对).
// The whole text is read first; rules on its words decide the type, without
// asking the model again; only a typed photo has its fields read, with the
// type's template; then every field is checked. A field is either passed or
// left for a person to check, and its value is never changed: the models
// get about one number field in five not quite right, and a few of those
// look valid.
package textread

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Kind names a document type; Plain is text that is no known document.
type Kind string

const (
	Plain       Kind = "plain"
	IDCardFront Kind = "id_card"
	IDCardBack  Kind = "id_card_back"
	BankCard    Kind = "bank_card"
	BankSlip    Kind = "bank_slip"
	Invoice     Kind = "invoice"
	Receipt     Kind = "receipt"
)

// documents lists the types in the order their rules are tried: the most
// specific first, receipts, whose words other documents share, last.
var documents = []struct {
	kind   Kind
	fields []string
	match  func(text string) bool
}{
	{IDCardFront, []string{"姓名", "性别", "民族", "出生", "住址", "公民身份号码"},
		func(t string) bool { return strings.Contains(t, "公民身份号码") }},
	{IDCardBack, []string{"签发机关", "有效期限"},
		func(t string) bool { return strings.Contains(t, "签发机关") && strings.Contains(t, "有效期限") }},
	{Invoice, []string{"发票号码", "开票日期", "购买方", "销售方", "金额", "税额", "价税合计"},
		func(t string) bool { return strings.Contains(t, "发票代码") || strings.Contains(t, "发票号码") }},
	{BankSlip, []string{"付款人", "付款账号", "收款人", "收款账号", "金额", "日期", "交易流水号"},
		func(t string) bool {
			return strings.Contains(t, "回单") || strings.Contains(t, "交易流水") || strings.Contains(t, "转账凭证")
		}},
	{BankCard, []string{"卡号", "发卡行"}, looksLikeBankCard},
	{Receipt, []string{"商户", "日期", "合计"},
		func(t string) bool {
			return strings.Contains(t, "合计") || strings.Contains(t, "实收") || strings.Contains(t, "收据") ||
				strings.Contains(t, "小票")
		}},
}

var cardNumber = regexp.MustCompile(`\d[\d ]{14,22}\d`)

// looksLikeBankCard: a card's few words, a bank's name and a number that
// passes the Luhn check.
func looksLikeBankCard(text string) bool {
	bank := strings.Contains(text, "银行") || strings.Contains(text, "银联") || strings.Contains(text, "unionpay")
	return bank && len([]rune(text)) <= 300 && slices.ContainsFunc(cardNumber.FindAllString(text, -1), luhn)
}

// Classify returns the document type the whole text shows, or Plain.
func Classify(text string) Kind {
	squeezed := squeeze(text)
	for _, document := range documents {
		if document.match(squeezed) {
			return document.kind
		}
	}
	return Plain
}

// Template is the JSON object whose empty values the reader fills for kind,
// or nil for Plain.
func Template(kind Kind) json.RawMessage {
	for _, document := range documents {
		if document.kind == kind {
			template := make(map[string]string, len(document.fields))
			for _, field := range document.fields {
				template[field] = ""
			}
			data, _ := json.Marshal(template)
			return data
		}
	}
	return nil
}

// Token is one token of the fields' reading with its log probability.
type Token struct {
	Text    string
	LogProb float64
}

// Check says whether a field passed or why a person should look at it.
type Check struct {
	Passed bool `json:"passed"`
	// Reasons are why it did not pass: not_in_text, format, mismatch (it
	// disagrees with another field), low_confidence.
	Reasons []string `json:"reasons,omitempty"`
}

// Options set the checks that depend on measurements.
type Options struct {
	// MinConfidence is the probability every token of a value must reach;
	// 0 leaves it out.
	MinConfidence float64
}

// Fields parses the reader's answer for kind: the template's fields with a
// value, everything else dropped.
func Fields(kind Kind, answer string) map[string]string {
	// The object, without a Markdown fence the model may put around it.
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end < start {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(answer[start:end+1]), &parsed); err != nil {
		return nil
	}
	var template map[string]string
	if err := json.Unmarshal(Template(kind), &template); err != nil {
		return nil
	}
	fields := make(map[string]string)
	for key := range template {
		value, ok := parsed[key].(string)
		if !ok {
			if number, isNumber := parsed[key].(float64); isNumber {
				value = strconv.FormatFloat(number, 'f', -1, 64)
			}
		}
		if value = strings.TrimSpace(value); value != "" && !empty(value) {
			fields[key] = value
		}
	}
	return fields
}

func empty(value string) bool {
	switch strings.ToLower(value) {
	case "无", "空", "-", "none", "null", "n/a":
		return true
	}
	return false
}

// Verify checks each field of kind against the whole text, its format and
// the other fields, and the tokens that spell it.
func Verify(kind Kind, fields map[string]string, text string, tokens []Token, options Options) map[string]Check {
	whole := squeeze(text)
	spelled := spelledBy(tokens)
	checks := make(map[string]Check, len(fields))
	for key, value := range fields {
		var reasons []string
		if !strings.Contains(whole, squeeze(value)) {
			reasons = append(reasons, "not_in_text")
		}
		if !wellFormed(key, value) {
			reasons = append(reasons, "format")
		}
		if options.MinConfidence > 0 && spelled(value) < options.MinConfidence {
			reasons = append(reasons, "low_confidence")
		}
		checks[key] = Check{Passed: len(reasons) == 0, Reasons: reasons}
	}
	for _, key := range disagreeing(kind, fields) {
		check := checks[key]
		check.Passed = false
		check.Reasons = append(check.Reasons, "mismatch")
		checks[key] = check
	}
	return checks
}

// squeeze compares text regardless of spaces, width and case.
func squeeze(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(text))
}

// spelledBy returns the lowest probability among the tokens that spell a
// value in the reading, or 1 when the value cannot be found in it.
func spelledBy(tokens []Token) func(string) float64 {
	var reading strings.Builder
	starts := make([]int, len(tokens))
	for i, token := range tokens {
		starts[i] = reading.Len()
		reading.WriteString(token.Text)
	}
	text := reading.String()
	return func(value string) float64 {
		start := strings.Index(text, value)
		if start < 0 || value == "" {
			return 1
		}
		end, lowest := start+len(value), 0.0
		for i, token := range tokens {
			tokenEnd := starts[i] + len(token.Text)
			if tokenEnd > start && starts[i] < end {
				lowest = math.Min(lowest, token.LogProb)
			}
		}
		return math.Exp(lowest)
	}
}
