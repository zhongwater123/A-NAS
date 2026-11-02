package textread

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	idNumber = regexp.MustCompile(`^\d{17}[\dXx]$`)
	// A date as documents write it, a time of day may follow.
	dateValue = regexp.MustCompile(`^(\d{4})\s*[-/.年]\s*(\d{1,2})\s*[-/.月]\s*(\d{1,2})日?`)
	compact   = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})$`)
	validity  = regexp.MustCompile(`^(.+?)\s*[-—–至~]\s*(.+)$`)
	amount    = regexp.MustCompile(`^[-+]?[¥￥]?\s*(\d{1,3}(,\d{3})+|\d+)(\.\d{1,2})?\s*元?$`)
	invoiceNo = regexp.MustCompile(`^(\d{8}|\d{20})$`)
)

// wellFormed checks a field's value against what its name says it is; a
// field without a known format passes.
func wellFormed(key, value string) bool {
	switch key {
	case "公民身份号码":
		return idNumberValid(value)
	case "卡号":
		return luhn(value)
	case "出生", "开票日期", "日期":
		_, ok := parseDate(value)
		return ok
	case "有效期限":
		return validityValid(value)
	case "发票号码":
		return invoiceNo.MatchString(strings.ReplaceAll(value, " ", ""))
	case "金额", "税额", "价税合计", "合计":
		_, ok := parseAmount(value)
		return ok
	case "性别":
		return value == "男" || value == "女"
	}
	return true
}

// disagreeing returns the fields that contradict each other: an ID number's
// birth date or sex against the card's, an invoice's total against its
// amount and tax.
func disagreeing(kind Kind, fields map[string]string) []string {
	var keys []string
	switch kind {
	case IDCardFront:
		number := strings.ToUpper(fields["公民身份号码"])
		if !idNumberValid(number) {
			break
		}
		if born, ok := parseDate(fields["出生"]); ok && born.Format("20060102") != number[6:14] {
			keys = append(keys, "出生", "公民身份号码")
		}
		if sex := fields["性别"]; sex == "男" || sex == "女" {
			if male := (number[16]-'0')%2 == 1; male != (sex == "男") {
				keys = append(keys, "性别", "公民身份号码")
			}
		}
	case Invoice:
		net, okNet := parseAmount(fields["金额"])
		tax, okTax := parseAmount(fields["税额"])
		total, okTotal := parseAmount(fields["价税合计"])
		if okNet && okTax && okTotal && math.Abs(net+tax-total) > 0.005 {
			keys = append(keys, "金额", "税额", "价税合计")
		}
	}
	return keys
}

// idNumberValid checks a resident ID number's GB 11643 check digit and the
// birth date it holds.
func idNumberValid(value string) bool {
	value = strings.ToUpper(strings.ReplaceAll(value, " ", ""))
	if !idNumber.MatchString(value) {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, weight := range weights {
		sum += int(value[i]-'0') * weight
	}
	if "10X98765432"[sum%11] != value[17] {
		return false
	}
	_, ok := parseDate(value[6:14])
	return ok
}

// luhn checks a card number's last digit.
func luhn(value string) bool {
	digits := strings.ReplaceAll(value, " ", "")
	if len(digits) < 12 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		digit := int(digits[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		if (len(digits)-1-i)%2 == 1 {
			if digit *= 2; digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

// parseDate reads a date that exists, as documents write it.
func parseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	parts := dateValue.FindStringSubmatch(value)
	if parts == nil {
		parts = compact.FindStringSubmatch(value)
	}
	if parts == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(parts[1])
	month, _ := strconv.Atoi(parts[2])
	day, _ := strconv.Atoi(parts[3])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	// time.Date normalises 2月30日 to 3月2日: such a date does not exist.
	if date.Year() != year || int(date.Month()) != month || date.Day() != day || year < 1900 || year > 2200 {
		return time.Time{}, false
	}
	return date, true
}

// validityValid reads an ID card's term: two dates, or a start and 长期.
func validityValid(value string) bool {
	parts := validity.FindStringSubmatch(strings.TrimSpace(value))
	if parts == nil {
		return false
	}
	start, ok := parseDate(parts[1])
	if !ok {
		return false
	}
	if strings.TrimSpace(parts[2]) == "长期" {
		return true
	}
	end, ok := parseDate(parts[2])
	return ok && end.After(start)
}

// parseAmount reads a money amount: digits with an optional currency sign,
// thousands separators, up to two decimals and 元.
func parseAmount(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if !amount.MatchString(value) {
		return 0, false
	}
	digits := strings.NewReplacer("¥", "", "￥", "", ",", "", "元", "", " ", "").Replace(value)
	number, err := strconv.ParseFloat(digits, 64)
	return number, err == nil
}
