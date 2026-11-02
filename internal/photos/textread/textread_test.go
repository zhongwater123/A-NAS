package textread_test

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

// Synthetic numbers with valid check digits; no real person's.
const (
	femaleID = "11010519491231002X" // born 1949-12-31
	maleID   = "440306199001011236" // born 1990-01-01
	card     = "6228480012345678903"
)

func TestTheWholeTextDecidesTheDocumentType(t *testing.T) {
	for _, tc := range []struct {
		text string
		want textread.Kind
	}{
		{"姓名 张三\n性别 男 民族 汉\n公民身份号码 " + maleID, textread.IDCardFront},
		{"中华人民共和国\n居民身份证\n签发机关 某市公安局\n有效期限 2010.01.01-2030.01.01", textread.IDCardBack},
		{"电子发票（普通发票）\n发票号码：24312000000012345678\n开票日期：2024年05月06日", textread.Invoice},
		{"中国工商银行 电子回单\n付款人 张三", textread.BankSlip},
		{"中国农业银行 借记卡\n" + card[:4] + " " + card[4:8] + " " + card[8:12] + " " + card[12:16] + " " + card[16:], textread.BankCard},
		{"某某超市\n合计 ¥35.50\n实收 50.00", textread.Receipt},
		{"欢迎光临\n营业时间 9:00-21:00", textread.Plain},
		// A long text with a card-like number is no card.
		{"中国银行 " + card + " " + string(make([]rune, 400)), textread.Plain},
	} {
		if got := textread.Classify(tc.text); got != tc.want {
			t.Errorf("Classify(%.30q) = %s, want %s", tc.text, got, tc.want)
		}
	}
}

func TestATypeHasATemplateAndPlainTextNone(t *testing.T) {
	var template map[string]string
	if err := json.Unmarshal(textread.Template(textread.BankCard), &template); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(template)); !slices.Equal(keys, []string{"卡号", "发卡行"}) || template["卡号"] != "" {
		t.Fatalf("bank card template = %v", template)
	}
	if textread.Template(textread.Plain) != nil {
		t.Fatal("plain text has a template")
	}
}

func TestFieldsKeepTheTemplatesFilledValues(t *testing.T) {
	answer := "```json\n{\"商户\": \"某某超市\", \"日期\": \"无\", \"合计\": 35.5, \"店员\": \"李四\"}\n```"
	got := textread.Fields(textread.Receipt, answer)
	if want := map[string]string{"商户": "某某超市", "合计": "35.5"}; !maps.Equal(got, want) {
		t.Fatalf("Fields() = %v, want %v", got, want)
	}
	if got := textread.Fields(textread.Receipt, "not json"); len(got) != 0 {
		t.Fatalf("Fields(not json) = %v", got)
	}
}

func reasons(checks map[string]textread.Check, key string) []string {
	if checks[key].Passed {
		return nil
	}
	return checks[key].Reasons
}

func TestFieldsPassOnlyWhenEveryCheckHolds(t *testing.T) {
	text := "姓名 张三 性别 男 出生 1990年1月1日 住址 某市某路1号 公民身份号码 " + maleID
	fields := map[string]string{"姓名": "张三", "性别": "男", "出生": "1990年1月1日", "公民身份号码": maleID, "住址": "某市某路2号"}
	checks := textread.Verify(textread.IDCardFront, fields, text, nil, textread.Options{})
	for _, key := range []string{"姓名", "性别", "出生", "公民身份号码"} {
		if !checks[key].Passed {
			t.Errorf("%s did not pass: %v", key, checks[key].Reasons)
		}
	}
	// Read differently from the whole text: a person looks.
	if got := reasons(checks, "住址"); !slices.Equal(got, []string{"not_in_text"}) {
		t.Errorf("住址 reasons = %v", got)
	}

	// A wrong check digit, and a birth date and sex the number contradicts.
	wrong := maleID[:17] + "7"
	fields = map[string]string{"公民身份号码": wrong, "性别": "男"}
	checks = textread.Verify(textread.IDCardFront, fields, wrong+" 男", nil, textread.Options{})
	if got := reasons(checks, "公民身份号码"); !slices.Equal(got, []string{"format"}) {
		t.Errorf("wrong ID number reasons = %v", got)
	}
	fields = map[string]string{"公民身份号码": femaleID, "性别": "男", "出生": "1949年12月30日"}
	checks = textread.Verify(textread.IDCardFront, fields, femaleID+" 男 1949年12月30日", nil, textread.Options{})
	for _, key := range []string{"公民身份号码", "性别", "出生"} {
		if got := reasons(checks, key); !slices.Contains(got, "mismatch") {
			t.Errorf("%s reasons = %v, want mismatch", key, got)
		}
	}
}

func TestFormatsAndSumsOfCardsAndInvoices(t *testing.T) {
	checks := textread.Verify(textread.BankCard, map[string]string{"卡号": card[:18] + "4"}, card[:18]+"4", nil, textread.Options{})
	if got := reasons(checks, "卡号"); !slices.Equal(got, []string{"format"}) {
		t.Errorf("card number with a wrong Luhn digit: reasons = %v", got)
	}
	invoice := map[string]string{"发票号码": "24312000000012345678", "开票日期": "2024年02月30日",
		"金额": "100.00", "税额": "13.00", "价税合计": "¥113.10"}
	text := "发票号码 24312000000012345678 开票日期 2024年02月30日 金额 100.00 税额 13.00 价税合计 ¥113.10"
	checks = textread.Verify(textread.Invoice, invoice, text, nil, textread.Options{})
	if !checks["发票号码"].Passed {
		t.Errorf("发票号码 reasons = %v", checks["发票号码"].Reasons)
	}
	if got := reasons(checks, "开票日期"); !slices.Equal(got, []string{"format"}) {
		t.Errorf("a date that does not exist: reasons = %v", got)
	}
	for _, key := range []string{"金额", "税额", "价税合计"} {
		if got := reasons(checks, key); !slices.Equal(got, []string{"mismatch"}) {
			t.Errorf("%s reasons = %v, want the sum's mismatch", key, got)
		}
	}
}

func TestLowConfidenceIsCheckedOnlyWhenAsked(t *testing.T) {
	tokens := []textread.Token{{`{"合计": "`, 0}, {"35", math.Log(0.999)}, {".5", math.Log(0.6)}, {`"}`, 0}}
	fields := map[string]string{"合计": "35.5"}
	if checks := textread.Verify(textread.Receipt, fields, "合计 35.5", tokens, textread.Options{}); !checks["合计"].Passed {
		t.Fatalf("without a minimum: reasons = %v", checks["合计"].Reasons)
	}
	checks := textread.Verify(textread.Receipt, fields, "合计 35.5", tokens, textread.Options{MinConfidence: 0.99})
	if got := reasons(checks, "合计"); !slices.Equal(got, []string{"low_confidence"}) {
		t.Fatalf("with 0.99: reasons = %v", got)
	}
}
