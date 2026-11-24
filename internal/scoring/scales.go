package scoring

// Scale is how a batch is scored, every 打分项 of it alike. Scores stay in
// hundredths from 0 to MaxScore; a scale lets only some of them through.
type Scale string

const (
	// Hundredths is the number line from 0 to 5 with two decimals.
	Hundredths Scale = "hundredths"
	// Tenths is the number line from 0 to 5 with one decimal.
	Tenths Scale = "tenths"
	// Whole is the number line from 0 to 5 in whole points.
	Whole Scale = "whole"
	// Three is three levels: 差 1, 中 3 and 好 5.
	Three Scale = "three"
	// Five is five levels, the whole points without 0: 很差 1, 较差 2,
	// 一般 3, 好 4 and 很好 5.
	Five Scale = "five"
)

func (scale Scale) valid() bool {
	switch scale {
	case Hundredths, Tenths, Whole, Three, Five:
		return true
	}
	return false
}

// HasDecimals tells the scales whose decimals a batch can keep from raters.
func (scale Scale) HasDecimals() bool { return scale == Hundredths || scale == Tenths }

// Places is how many decimals a score of the scale has.
func (scale Scale) Places() int {
	switch scale {
	case Hundredths:
		return 2
	case Tenths:
		return 1
	}
	return 0
}

// fits tells whether a score in hundredths, 0 to MaxScore, is one of the
// scale's.
func (scale Scale) fits(score int) bool {
	switch scale {
	case Tenths:
		return score%10 == 0
	case Whole:
		return score%100 == 0
	case Three:
		return score == 100 || score == 300 || score == 500
	case Five:
		return score >= 100 && score%100 == 0
	}
	return true
}

// describe says in the export what a score of the scale is on a 打分项:
// levels name 情绪 from calm to intense, every other one from poor to good,
// as the window does (web/src/scoring/model.ts, levelsFor).
func (scale Scale) describe(dimension string) string {
	switch {
	case scale == Tenths:
		return "0 到 5，一位小数"
	case scale == Whole:
		return "0 到 5 的整数"
	case scale == Three && dimension == Emotion:
		return "三档：1 平静，3 中间，5 激烈"
	case scale == Three:
		return "三档：1 差，3 中，5 好"
	case scale == Five && dimension == Emotion:
		return "五档：1 很平静，2 安静，3 中间，4 有张力，5 激烈"
	case scale == Five:
		return "五档：1 很差，2 较差，3 一般，4 好，5 很好"
	}
	return "0 到 5，两位小数"
}

// says is how an error names the scale's scores.
func (scale Scale) says() string {
	switch scale {
	case Tenths:
		return "0 to 5 in steps of 0.1"
	case Whole:
		return "a whole number from 0 to 5"
	case Three:
		return "1, 3 or 5"
	case Five:
		return "a whole number from 1 to 5"
	}
	return "0 to 5"
}
