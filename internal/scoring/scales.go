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
)

// Scales lists every scale in the order the window offers them.
var Scales = []Scale{Hundredths, Tenths, Whole, Three}

func (scale Scale) valid() bool {
	switch scale {
	case Hundredths, Tenths, Whole, Three:
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
	}
	return true
}

// describe says in the export what a score of the scale is.
func (scale Scale) describe() string {
	switch scale {
	case Tenths:
		return "0 到 5，一位小数"
	case Whole:
		return "0 到 5 的整数"
	case Three:
		return "三档：1 差，3 中，5 好"
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
	}
	return "0 to 5"
}
