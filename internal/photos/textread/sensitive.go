package textread

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Sensitive values (docs/architecture/photo-ocr.md#ai-助手能用看不到具体的值)
// are shown masked, and a member's AI assistant gets them only as handles:
// the fields the sensitive-field list names, and the numbers its rules find
// in the whole text.

// sensitiveFields maps each sensitive field to the label its handle shows.
var sensitiveFields = map[string]string{
	"公民身份号码": "身份证号",
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

// How a number may be printed: ASCII or full-width digits, in groups split by
// spaces of any kind, line breaks or hyphens, and an ID number's final X.
// Search compares regardless of width and spaces (Fold), so whatever it would
// join into one number must be one here too, or an assistant could find a
// number it does not see a part at a time.
const (
	digitClass     = `0-9\x{FF10}-\x{FF19}`
	separatorClass = `\s\x{00A0}\x{3000}\-\x{2010}-\x{2015}\x{2212}\x{FF0D}`
	finalX         = `Xx\x{FF38}\x{FF58}`
)

var (
	// A run of digits: at least 11, a mobile number's length.
	digitRun = regexp.MustCompile(`[` + digitClass + `](?:[` + separatorClass + `]*[` + digitClass + `]){10,}` +
		`(?:[` + separatorClass + `]*[` + finalX + `])?`)
	// A group of a run: digits printed together.
	digitGroup = regexp.MustCompile(`[` + digitClass + `]+[` + finalX + `]?`)
)

// Numbers finds the sensitive numbers in text: ID numbers whose check digit
// holds, card numbers that pass the Luhn check and mobile numbers, and any
// other stretch of 15 or more digits, which a misread ID or card number with
// a wrong check digit still is. Numbers printed side by side, such as a phone
// number and a date, are told apart by their groups: at each group, the
// longest run of groups that is a known number is one, and the groups between
// such numbers are another when they hold 15 digits or more.
func Numbers(text string) []Number {
	var found []Number
	for _, span := range digitRun.FindAllStringIndex(text, -1) {
		groups := digitGroup.FindAllStringIndex(text[span[0]:span[1]], -1)
		sizes := make([]int, len(groups))
		for k, group := range groups {
			group[0] += span[0]
			group[1] += span[0]
			sizes[k] = len(digitsOf(text[group[0]:group[1]]))
		}
		stretch := -1 // the first group of the stretch no number has taken
		unknown := func(end int) {
			if stretch < 0 {
				return
			}
			start, stop := groups[stretch][0], groups[end-1][1]
			if digits := digitsOf(text[start:stop]); len(digits) >= 15 {
				found = append(found, Number{Label: "号码", Digits: digits, Start: start, End: stop})
			}
			stretch = -1
		}
		for i := 0; i < len(groups); {
			// No known number is longer than 19 digits.
			j, digits := i, 0
			for j < len(groups) && digits+sizes[j] <= 19 {
				digits += sizes[j]
				j++
			}
			for ; j > i; j-- {
				if number, ok := known(text, groups[i][0], groups[j-1][1]); ok {
					unknown(i)
					found = append(found, number)
					break
				}
			}
			if j > i {
				i = j
				continue
			}
			if stretch < 0 {
				stretch = i
			}
			i++
		}
		unknown(len(groups))
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

// digitsOf is a number as printed, in ASCII digits and an upper-case X,
// without what separates its groups.
func digitsOf(printed string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r == 'X':
			return r
		case r == 'x':
			return 'X'
		}
		return -1
	}, norm.NFKC.String(printed))
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
