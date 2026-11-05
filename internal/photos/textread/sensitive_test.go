package textread_test

import (
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

func TestNumbersFindsWhatIsSensitiveByItsRules(t *testing.T) {
	text := "公民身份号码 " + maleID + " 卡号 6228 4800 1234 5678 903 电话 138-1234-5678\n" +
		"错一位的号码 440306199001011238 金额 35.50 日期 20240506 发票号码 12345678 电话 13812345678 20240506"
	var found []string
	for _, number := range textread.Numbers(text) {
		found = append(found, number.Label+":"+number.Digits)
		if text[number.Start:number.End] == "" {
			t.Errorf("%+v spans nothing", number)
		}
	}
	want := []string{"身份证号:" + maleID, "卡号:" + card, "手机号:13812345678", "号码:440306199001011238",
		"手机号:13812345678"}
	if !slices.Equal(found, want) {
		t.Fatalf("Numbers() = %v, want %v", found, want)
	}
}

func TestMaskingLeavesNoDigitOfASensitiveValue(t *testing.T) {
	text := "住址 某市 某路1号\n公民身份号码 " + maleID + "\n电话 138 1234 5678"
	masked := textread.MaskValue(text, "某市某路1号", func() string { return "[住址]" })
	masked = textread.MaskNumbers(masked, func(number textread.Number) string { return "[" + number.Label + "]" })
	if want := "住址 [住址]\n公民身份号码 [身份证号]\n电话 [手机号]"; masked != want {
		t.Fatalf("masked = %q, want %q", masked, want)
	}
	if textread.MaskValue(text, "  ", func() string { return "x" }) != text {
		t.Fatal("an empty value masked something")
	}
}

func TestANumericQueryIgnoresSpacesAndHyphens(t *testing.T) {
	for query, numeric := range map[string]bool{"6222 0200": true, "138-1234": true, "发票 2024": true, "发票": false,
		"123": false, "城南初中": false} {
		if got := textread.Numeric(query); got != numeric {
			t.Errorf("Numeric(%q) = %v, want %v", query, got, numeric)
		}
	}
	if got := textread.FoldNumber("６２２２ 0200-1234"); got != "622202001234" {
		t.Fatalf("FoldNumber() = %q", got)
	}
}
