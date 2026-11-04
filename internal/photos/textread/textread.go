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

// fieldsOf lists the fields of kind in the order the document shows them, or
// nil for Plain.
func fieldsOf(kind Kind) []string {
	for _, document := range documents {
		if document.kind == kind {
			return document.fields
		}
	}
	return nil
}

// Template is the JSON object whose empty values the reader fills for kind,
// or nil for Plain. Its keys keep the document's order, which the model reads
// in; a map would sort them.
func Template(kind Kind) json.RawMessage {
	fields := fieldsOf(kind)
	if fields == nil {
		return nil
	}
	var template strings.Builder
	template.WriteString("{")
	for i, field := range fields {
		if i > 0 {
			template.WriteString(", ")
		}
		template.WriteString(jsonString(field) + `: ""`)
	}
	template.WriteString("}")
	return json.RawMessage(template.String())
}

// jsonString is value as a JSON string, as a model writes it: without
// escaping HTML.
func jsonString(value string) string {
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(encoded.String(), "\n")
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
	// disagrees with another field), low_confidence, unfinished (the answer
	// was cut off or stopped looping).
	Reasons []string `json:"reasons,omitempty"`
}

// Options set the checks that depend on measurements and on how the answer
// ended.
type Options struct {
	// MinConfidence is the probability every token of a value must reach;
	// 0 leaves it out.
	MinConfidence float64
	// Unfinished says the answer did not end by itself: it reached the
	// length limit, or the Worker stopped it looping. None of its fields
	// passes.
	Unfinished bool
}

// Fields parses the reader's answer for kind: the template's fields with a
// value, everything else dropped.
func Fields(kind Kind, answer string) map[string]string {
	// The object, without a Markdown fence the model may put around it.
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end < start {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(answer[start : end+1]))
	// A number keeps the digits the model wrote: as a float, a card number
	// would be rounded.
	decoder.UseNumber()
	var parsed map[string]any
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	fields := make(map[string]string)
	for _, key := range fieldsOf(kind) {
		var value string
		switch written := parsed[key].(type) {
		case string:
			value = written
		case json.Number:
			value = written.String()
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
		if options.MinConfidence > 0 && spelled(key, value) < options.MinConfidence {
			reasons = append(reasons, "low_confidence")
		}
		if options.Unfinished {
			reasons = append(reasons, "unfinished")
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
// field's value where the reading gives it, after the field's key: another
// field's value may hold the same digits, as 113.00 holds 13.00. A value that
// cannot be found there, or a reading without tokens, gets 0: no token shows
// that the model was sure of it.
func spelledBy(tokens []Token) func(key, value string) float64 {
	var reading strings.Builder
	starts := make([]int, len(tokens))
	for i, token := range tokens {
		starts[i] = reading.Len()
		reading.WriteString(token.Text)
	}
	text := reading.String()
	return func(key, value string) float64 {
		name := jsonString(key)
		after := strings.Index(text, name)
		if after < 0 || value == "" {
			return 0
		}
		after += len(name)
		// As the model wrote it, or escaped within its JSON string.
		quoted := jsonString(value)
		start, written := -1, ""
		for _, written = range []string{value, quoted[1 : len(quoted)-1]} {
			if start = strings.Index(text[after:], written); start >= 0 {
				break
			}
		}
		if start < 0 {
			return 0
		}
		start += after
		end, lowest := start+len(written), 0.0
		for i, token := range tokens {
			tokenEnd := starts[i] + len(token.Text)
			if tokenEnd > start && starts[i] < end {
				lowest = math.Min(lowest, token.LogProb)
			}
		}
		return math.Exp(lowest)
	}
}
