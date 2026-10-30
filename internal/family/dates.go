package family

import (
	"time"
)

// dateLayout is how a calendar date travels and is stored: a birth date or
// the day something happened has no time of day and no zone.
const dateLayout = "2006-01-02"

// earliestDate bounds birth dates and record dates from below; anything
// earlier is a typo.
var earliestDate = time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC)

// parseDate reads a calendar date as midnight UTC.
func parseDate(value string) (time.Time, bool) {
	parsed, err := time.Parse(dateLayout, value)
	if err != nil || parsed.Before(earliestDate) {
		return time.Time{}, false
	}
	return parsed, true
}

// today is the device's current calendar date. The NAS and its family share
// a time zone, so the device's local date is the family's date.
func (s *Store) today() time.Time {
	now := s.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// Age is how old a baby is on a date: whole years, then whole months, then
// days, the way Chinese families count (“1 岁 2 个月 3 天”).
type Age struct {
	Years  int `json:"years"`
	Months int `json:"months"`
	Days   int `json:"days"`
	// TotalMonths is Years*12+Months: the month of life the date falls in,
	// starting at 0 for the first month.
	TotalMonths int `json:"totalMonths"`
}

// addMonths adds whole months to a birth date, keeping the day of month but
// clamping it to the month's last day, so a baby born on 31 January turns
// one month old on the last day of February rather than in March.
func addMonths(birth time.Time, months int) time.Time {
	first := time.Date(birth.Year(), birth.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(birth.Day(), last), 0, 0, 0, 0, time.UTC)
}

// ageOn returns the age on date and false when date is before birth.
func ageOn(birth, date time.Time) (Age, bool) {
	if date.Before(birth) {
		return Age{}, false
	}
	months := (date.Year()-birth.Year())*12 + int(date.Month()) - int(birth.Month())
	for months > 0 && addMonths(birth, months).After(date) {
		months--
	}
	days := int(date.Sub(addMonths(birth, months)).Hours() / 24)
	return Age{Years: months / 12, Months: months % 12, Days: days, TotalMonths: months}, true
}

// monthsOld is the age in fractional months that growth references use:
// whole months plus the days into the next one.
func monthsOld(birth, date time.Time) float64 {
	age, ok := ageOn(birth, date)
	if !ok {
		return 0
	}
	start := addMonths(birth, age.TotalMonths)
	length := addMonths(birth, age.TotalMonths+1).Sub(start).Hours() / 24
	return float64(age.TotalMonths) + float64(age.Days)/length
}
