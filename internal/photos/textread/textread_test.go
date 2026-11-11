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
	// The model fills the fields in the order the card shows them.
	if got, want := string(textread.Template(textread.IDCardFront)),
		`{"姓名": "", "性别": "", "民族": "", "出生": "", "住址": "", "公民身份号码": ""}`; got != want {
		t.Fatalf("ID card template = %s, want %s", got, want)
	}
}

func TestFieldsKeepTheTemplatesFilledValues(t *testing.T) {
	answer := "```json\n{\"商户\": \"某某超市\", \"日期\": \"无\", \"合计\": 35.50, \"店员\": \"李四\"}\n```"
	got := textread.Fields(textread.Receipt, answer)
	if want := map[string]string{"商户": "某某超市", "合计": "35.50"}; !maps.Equal(got, want) {
		t.Fatalf("Fields() = %v, want %v", got, want)
	}
	if got := textread.Fields(textread.Receipt, "not json"); len(got) != 0 {
		t.Fatalf("Fields(not json) = %v", got)
	}
	// A number written without quotes keeps its digits: as a float, a card
	// number would be rounded.
	if got := textread.Fields(textread.BankCard, `{"卡号": `+card+`}`); got["卡号"] != card {
		t.Fatalf("an unquoted card number = %v, want %s", got, card)
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
		// The number disagrees with both, and says so once.
		if got := reasons(checks, key); !slices.Equal(got, []string{"mismatch"}) {
			t.Errorf("%s reasons = %v, want mismatch", key, got)
		}
	}

	// A number read with spaces agrees with its birth date and sex all the same.
	spaced := maleID[:6] + " " + maleID[6:14] + " " + maleID[14:]
	fields = map[string]string{"公民身份号码": spaced, "性别": "男", "出生": "1990年1月1日"}
	checks = textread.Verify(textread.IDCardFront, fields, spaced+" 男 1990年1月1日", nil, textread.Options{})
	for key, check := range checks {
		if !check.Passed {
			t.Errorf("%s with a spaced ID number: reasons = %v", key, check.Reasons)
		}
	}
}

func TestAnIDCardsTermIsTwoDatesOrAStartAndLongTerm(t *testing.T) {
	for value, want := range map[string]bool{
		"2010.01.01-2030.01.01":     true,
		"2015-03-01至2035-03-01":     true,
		"2015-03-01-2035-03-01":     true,
		"20150301-20350301":         true,
		"2015.03.01-长期":             true,
		"2030.01.01-2010.01.01":     false,
		"2015.02.30-2035.02.28":     false,
		"有效期 2010.01.01-2030.01.01": false,
	} {
		checks := textread.Verify(textread.IDCardBack, map[string]string{"有效期限": value}, value, nil, textread.Options{})
		if got := checks["有效期限"].Passed; got != want {
			t.Errorf("有效期限 %q passed = %v, want %v: %v", value, got, want, checks["有效期限"].Reasons)
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

// sure and unsure are the log probabilities of a token read as 0.999 and 0.5.
var sure, unsure = math.Log(0.999), math.Log(0.5)

func TestAValueIsJudgedByTheTokensAfterItsKey(t *testing.T) {
	// 13.00 also sits inside 113.00, which comes first; the tax itself was a
	// guess.
	tokens := []textread.Token{
		{`{"价税合计": "`, 0}, {"1", sure}, {"1", sure}, {"3", sure}, {".00", sure},
		{`", "税额": "`, 0}, {"1", sure}, {"3", unsure}, {".00", sure}, {`"}`, 0},
	}
	fields := map[string]string{"价税合计": "113.00", "税额": "13.00"}
	checks := textread.Verify(textread.Invoice, fields, "价税合计 113.00 税额 13.00", tokens, textread.Options{MinConfidence: 0.99})
	if !checks["价税合计"].Passed {
		t.Errorf("价税合计 reasons = %v", checks["价税合计"].Reasons)
	}
	if got := reasons(checks, "税额"); !slices.Equal(got, []string{"low_confidence"}) {
		t.Errorf("税额 reasons = %v, want its own unsure token", got)
	}

	// A value written escaped in its JSON string is still found.
	tokens = []textread.Token{{`{"住址": "某路`, sure}, {`\"`, unsure}, {`1\"号"}`, sure}}
	checks = textread.Verify(textread.IDCardFront, map[string]string{"住址": `某路"1"号`}, `住址 某路"1"号`, tokens,
		textread.Options{MinConfidence: 0.99})
	if got := reasons(checks, "住址"); !slices.Equal(got, []string{"low_confidence"}) {
		t.Errorf("escaped 住址 reasons = %v", got)
	}
}

func TestAValueNoTokenSpellsDoesNotPass(t *testing.T) {
	fields := map[string]string{"合计": "35.50"}
	for name, tokens := range map[string][]textread.Token{
		"no tokens":         nil,
		"not in the tokens": {{`{"合计": "35.80"}`, 0}},
		"no key":            {{`35.50`, 0}},
	} {
		checks := textread.Verify(textread.Receipt, fields, "合计 35.50", tokens, textread.Options{MinConfidence: 0.99})
		if got := reasons(checks, "合计"); !slices.Equal(got, []string{"low_confidence"}) {
			t.Errorf("%s: reasons = %v", name, got)
		}
	}
}

func TestAnAnswerCutOffPassesNoField(t *testing.T) {
	checks := textread.Verify(textread.Receipt, map[string]string{"合计": "35.50"}, "合计 35.50", nil,
		textread.Options{Unfinished: true})
	if got := reasons(checks, "合计"); !slices.Equal(got, []string{"unfinished"}) {
		t.Fatalf("reasons = %v", got)
	}
}
