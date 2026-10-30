package family

import (
	"context"
	"embed"
	"encoding/csv"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

//go:embed standards/growth/*.csv
var growthFiles embed.FS

// Indicator is a measurement a growth standard has curves for.
type Indicator string

const (
	IndicatorHeight Indicator = "height"
	IndicatorWeight Indicator = "weight"
	IndicatorHead   Indicator = "head"
)

var indicators = []Indicator{IndicatorHeight, IndicatorWeight, IndicatorHead}

// GrowthPoint is the standard's percentile values at an age.
type GrowthPoint struct {
	AgeMonths float64   `json:"ageMonths"`
	Values    []float64 `json:"values"`
}

// standard is a growth standard: for each sex and indicator, percentile
// curves sampled at ages in months.
type standard struct {
	Name        string
	Percentiles []string
	curves      map[string]map[Indicator][]GrowthPoint
}

var growthStandard = mustStandard()

// mustStandard reads the shipped tables: one CSV per indicator and sex,
// named <indicator>-<sex>.csv, whose header is "months" and then the
// percentiles, the same in every file, from lowest to highest.
func mustStandard() standard {
	s := standard{Name: "WS/T 423—2022《7 岁以下儿童生长标准》", curves: map[string]map[Indicator][]GrowthPoint{}}
	for _, sex := range []string{"female", "male"} {
		s.curves[sex] = map[Indicator][]GrowthPoint{}
		for _, indicator := range indicators {
			name := fmt.Sprintf("standards/growth/%s-%s.csv", indicator, sex)
			raw, err := growthFiles.ReadFile(name)
			if err != nil {
				panic(err)
			}
			rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
			if err != nil || len(rows) < 2 || len(rows[0]) < 3 || rows[0][0] != "months" {
				panic("growth standard: malformed " + name)
			}
			if s.Percentiles == nil {
				s.Percentiles = rows[0][1:]
			} else if !slices.Equal(s.Percentiles, rows[0][1:]) {
				panic("growth standard: percentiles differ in " + name)
			}
			points := make([]GrowthPoint, 0, len(rows)-1)
			for _, row := range rows[1:] {
				point := GrowthPoint{}
				for i, field := range row {
					value, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
					if err != nil {
						panic(fmt.Sprintf("growth standard: %s: %q", name, field))
					}
					if i == 0 {
						point.AgeMonths = value
						continue
					}
					point.Values = append(point.Values, value)
				}
				if len(points) > 0 && (point.AgeMonths <= points[len(points)-1].AgeMonths || !slices.IsSorted(point.Values)) {
					panic(fmt.Sprintf("growth standard: %s is out of order at %v months", name, point.AgeMonths))
				}
				points = append(points, point)
			}
			s.curves[sex][indicator] = points
		}
	}
	return s
}

// at interpolates the percentile values linearly between the sampled ages;
// false means the age lies outside the standard.
func (s standard) at(sex string, indicator Indicator, months float64) ([]float64, bool) {
	points := s.curves[sex][indicator]
	if len(points) == 0 || months < points[0].AgeMonths || months > points[len(points)-1].AgeMonths {
		return nil, false
	}
	i, found := slices.BinarySearchFunc(points, months, func(point GrowthPoint, target float64) int {
		switch {
		case point.AgeMonths < target:
			return -1
		case point.AgeMonths > target:
			return 1
		}
		return 0
	})
	if found {
		return points[i].Values, true
	}
	before, after := points[i-1], points[i]
	share := (months - before.AgeMonths) / (after.AgeMonths - before.AgeMonths)
	values := make([]float64, len(before.Values))
	for j := range values {
		values[j] = before.Values[j] + share*(after.Values[j]-before.Values[j])
	}
	return values, true
}

// Assessment places a measurement among the standard's percentiles: between
// Lower and Upper, or below the lowest (no Lower) or above the highest (no
// Upper).
type Assessment struct {
	Value     float64 `json:"value"`
	Date      string  `json:"date"`
	AgeMonths float64 `json:"ageMonths"`
	Lower     string  `json:"lower,omitempty"`
	Upper     string  `json:"upper,omitempty"`
}

func (s standard) assess(sex string, indicator Indicator, months, value float64) (Assessment, bool) {
	values, ok := s.at(sex, indicator, months)
	if !ok {
		return Assessment{}, false
	}
	assessment := Assessment{Value: value, AgeMonths: months}
	for i, reference := range values {
		if value < reference {
			if i > 0 {
				assessment.Lower = s.Percentiles[i-1]
			}
			assessment.Upper = s.Percentiles[i]
			return assessment, true
		}
	}
	assessment.Lower = s.Percentiles[len(s.Percentiles)-1]
	return assessment, true
}

// GrowthMeasurement is one measurement on the curves.
type GrowthMeasurement struct {
	RecordID  string   `json:"recordId"`
	Date      string   `json:"date"`
	AgeMonths float64  `json:"ageMonths"`
	HeightCm  *float64 `json:"heightCm,omitempty"`
	WeightKg  *float64 `json:"weightKg,omitempty"`
	HeadCm    *float64 `json:"headCm,omitempty"`
}

// GrowthCurves are the standard's curves for one sex.
type GrowthCurves struct {
	Height []GrowthPoint `json:"height"`
	Weight []GrowthPoint `json:"weight"`
	Head   []GrowthPoint `json:"head"`
}

// LatestGrowth assesses the most recent value of each indicator.
type LatestGrowth struct {
	Height *Assessment `json:"height,omitempty"`
	Weight *Assessment `json:"weight,omitempty"`
	Head   *Assessment `json:"head,omitempty"`
}

// Growth is a baby's 成长记录: the measurements, oldest first, the
// standard's curves for the baby's sex, and where the latest values lie.
type Growth struct {
	Sex          string              `json:"sex"`
	BirthDate    string              `json:"birthDate"`
	Standard     string              `json:"standard"`
	Percentiles  []string            `json:"percentiles"`
	Curves       GrowthCurves        `json:"curves"`
	Measurements []GrowthMeasurement `json:"measurements"`
	Latest       LatestGrowth        `json:"latest"`
}

// Growth returns a baby's measurements against the growth standard.
func (s *Store) Growth(ctx context.Context, actor Actor, babyID string) (Growth, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Growth{}, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return Growth{}, err
	}
	rows, err := s.queryRows(ctx, s.db, "SELECT "+recordColumns+
		" FROM records WHERE baby_id = ? AND kind = 'measurement' AND deleted_at IS NULL ORDER BY occurred_on, created_at LIMIT 2000", babyID)
	if err != nil {
		return Growth{}, err
	}
	birth, _ := parseDate(baby.BirthDate)
	curves := growthStandard.curves[baby.Sex]
	growth := Growth{Sex: baby.Sex, BirthDate: baby.BirthDate, Standard: growthStandard.Name, Percentiles: growthStandard.Percentiles,
		Curves:       GrowthCurves{Height: curves[IndicatorHeight], Weight: curves[IndicatorWeight], Head: curves[IndicatorHead]},
		Measurements: []GrowthMeasurement{}}
	for _, r := range rows {
		m := r.record.Measurement
		date, _ := parseDate(r.record.OccurredOn)
		months := monthsOld(birth, date)
		growth.Measurements = append(growth.Measurements, GrowthMeasurement{RecordID: r.record.ID, Date: r.record.OccurredOn,
			AgeMonths: months, HeightCm: m.HeightCm, WeightKg: m.WeightKg, HeadCm: m.HeadCm})
		for indicator, value := range map[Indicator]*float64{IndicatorHeight: m.HeightCm, IndicatorWeight: m.WeightKg, IndicatorHead: m.HeadCm} {
			if value == nil {
				continue
			}
			assessment, ok := growthStandard.assess(baby.Sex, indicator, months, *value)
			if !ok {
				assessment = Assessment{Value: *value, AgeMonths: months}
			}
			assessment.Date = r.record.OccurredOn
			switch indicator {
			case IndicatorHeight:
				growth.Latest.Height = &assessment
			case IndicatorWeight:
				growth.Latest.Weight = &assessment
			case IndicatorHead:
				growth.Latest.Head = &assessment
			}
		}
	}
	return growth, nil
}
