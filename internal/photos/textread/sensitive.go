package textread

import (
	"regexp"
	"strings"
	"unicode"
)

// Sensitive values (docs/architecture/photo-ocr.md#ai-助手能用看不到具体的值)
// are shown masked, and a member's AI assistant gets them only as handles:
// the fields the sensitive-field list names, and the numbers its rules find
// in the whole text.

// sensitiveFields maps each sensitive field to the label its handle shows.
var sensitiveFields = map[string]string{
	"公民身份号码": "身份证号",
	"住址":     "住址",
	"卡号":     "卡号",
	"付款账号":   "账号",
	"收款账号":   "账号",
	"交易流水号":  "流水号",
}

// Sensitive reports whether a field holds a sensitive value, and the label
// its handle shows.
func Sensitive(field string) (label string, ok bool) {
	label, ok = sensitiveFields[field]
	return label, ok
}

// FieldNames lists the fields of kind in the order the document shows them,
// or nil for Plain.
func FieldNames(kind Kind) []string {
	return fieldsOf(kind)
}

// A Number is a sensitive number found in a text by its rules.
type Number struct {
	// Label is what a handle for it shows: 身份证号, 卡号, 手机号 or 号码.
	Label string
	// Digits is the number without its spaces and hyphens.
	Digits string
	// Start and End are its byte offsets in the text.
	Start, End int
}

// A run of digits a number may be printed as: groups split by single spaces
// or hyphens, as cards and phone numbers are, and an ID number's final X.
var digitRun = regexp.MustCompile(`[0-9](?:[ -]?[0-9]){10,}(?:[ -]?[Xx])?`)

// Numbers finds the sensitive numbers in text: ID numbers whose check digit
// holds, card numbers that pass the Luhn check and mobile numbers, and any
// other run of 15 or more digits, which a misread ID or card number with a
// wrong check digit still is. Numbers printed side by side, such as a phone
// number and a date, are told apart by their groups.
func Numbers(text string) []Number {
	var found []Number
	for _, span := range digitRun.FindAllStringIndex(text, -1) {
		if number, ok := known(text, span[0], span[1]); ok {
			found = append(found, number)
			continue
		}
		var parts []Number
		start := span[0]
		for i := span[0]; i <= span[1]; i++ {
			if i == span[1] || text[i] == ' ' || text[i] == '-' {
				if number, ok := known(text, start, i); ok {
					parts = append(parts, number)
				}
				start = i + 1
			}
		}
		if digits := digitsOf(text[span[0]:span[1]]); len(parts) == 0 && len(digits) >= 15 {
			parts = append(parts, Number{Label: "号码", Digits: digits, Start: span[0], End: span[1]})
		}
		found = append(found, parts...)
	}
	return found
}

// known names the sensitive number text[start:end] is by its rules, if any.
func known(text string, start, end int) (Number, bool) {
	digits := digitsOf(text[start:end])
	number := Number{Digits: digits, Start: start, End: end}
	switch {
	case len(digits) == 18 && idNumberValid(digits):
		number.Label = "身份证号"
	case len(digits) >= 16 && len(digits) <= 19 && luhn(digits):
		number.Label = "卡号"
	case len(digits) == 11 && digits[0] == '1' && digits[1] >= '3' && digits[1] <= '9':
		number.Label = "手机号"
	default:
		return Number{}, false
	}
	return number, true
}

// digitsOf is a number as printed, without its spaces and hyphens.
func digitsOf(printed string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(printed))
}

// MaskValue replaces each occurrence of value in text, allowing for spaces
// between its characters, with what replace returns.
func MaskValue(text, value string, replace func() string) string {
	if strings.TrimSpace(value) == "" {
		return text
	}
	return spaced(value).ReplaceAllStringFunc(text, func(string) string { return replace() })
}

// MaskNumbers replaces each number Numbers finds in text with what replace
// returns for it.
func MaskNumbers(text string, replace func(Number) string) string {
	var masked strings.Builder
	last := 0
	for _, number := range Numbers(text) {
		masked.WriteString(text[last:number.Start])
		masked.WriteString(replace(number))
		last = number.End
	}
	masked.WriteString(text[last:])
	return masked.String()
}

// spaced matches value with any spaces between its characters.
func spaced(value string) *regexp.Regexp {
	var pattern strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) {
			continue
		}
		if pattern.Len() > 0 {
			pattern.WriteString(`\s*`)
		}
		pattern.WriteString(regexp.QuoteMeta(string(r)))
	}
	return regexp.MustCompile(pattern.String())
}

// Fold compares text regardless of spaces, width and case, as search does.
func Fold(text string) string {
	return squeeze(text)
}

// FoldNumber folds a query or text full of digits further, without hyphens,
// so that "6222 0200" finds 6222-0200.
func FoldNumber(text string) string {
	return strings.ReplaceAll(squeeze(text), "-", "")
}

// Numeric reports whether a query is mostly digits: at least four, and at
// least half of what it holds besides spaces and hyphens.
func Numeric(query string) bool {
	folded := FoldNumber(query)
	digits := 0
	for _, r := range folded {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 4 && 2*digits >= len([]rune(folded))
}
