package family

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SuggestedMilestones are offered when adding a milestone; anyone may type
// another title.
var SuggestedMilestones = []string{
	"第一次笑", "第一次翻身", "第一次独坐", "第一次爬", "长出第一颗牙", "第一次站立", "迈出第一步",
	"第一次叫爸爸", "第一次叫妈妈", "第一次剪头发", "第一次吃辅食", "断奶", "上幼儿园", "上小学",
}

// AutoMilestone is a day every baby has, worked out from the birth date.
type AutoMilestone struct {
	Key   string    `json:"key"`
	Title string    `json:"title"`
	Date  time.Time `json:"date"`
}

// autoMilestone finds an automatic milestone by key: birth, the first full
// month, the hundredth day counting the birth day as the first, the first
// birthday and every later one ("birthday-N").
func autoMilestone(birth time.Time, key string) (AutoMilestone, bool) {
	switch key {
	case "birth":
		return AutoMilestone{Key: key, Title: "出生", Date: birth}, true
	case "month-1":
		return AutoMilestone{Key: key, Title: "满月", Date: addMonths(birth, 1)}, true
	case "day-100":
		return AutoMilestone{Key: key, Title: "百天", Date: birth.AddDate(0, 0, 99)}, true
	case "year-1":
		return AutoMilestone{Key: key, Title: "周岁", Date: addMonths(birth, 12)}, true
	}
	if rest, ok := strings.CutPrefix(key, "birthday-"); ok {
		years, err := strconv.Atoi(rest)
		if err == nil && years >= 2 && years <= 100 && strconv.Itoa(years) == rest {
			return AutoMilestone{Key: key, Title: fmt.Sprintf("%d 岁生日", years), Date: addMonths(birth, 12*years)}, true
		}
	}
	return AutoMilestone{}, false
}

// autoMilestones lists the automatic milestones up to today and the next one
// still to come.
func autoMilestones(birth, today time.Time) []AutoMilestone {
	keys := []string{"birth", "month-1", "day-100", "year-1"}
	for years := 2; years <= 100; years++ {
		keys = append(keys, "birthday-"+strconv.Itoa(years))
	}
	var milestones []AutoMilestone
	for _, key := range keys {
		milestone, _ := autoMilestone(birth, key)
		milestones = append(milestones, milestone)
		if milestone.Date.After(today) {
			break
		}
	}
	slices.SortStableFunc(milestones, func(a, b AutoMilestone) int { return a.Date.Compare(b.Date) })
	return milestones
}

// TimelineEntry is one line of the 大事记 timeline: an automatic milestone,
// a member's milestone, or an automatic one that a member annotated.
type TimelineEntry struct {
	Date string `json:"date"`
	Age  Age    `json:"age"`
	// Auto is the automatic milestone's key, when the entry is one.
	Auto  string `json:"auto,omitempty"`
	Title string `json:"title"`
	// Upcoming marks the next automatic milestone, still to come.
	Upcoming bool `json:"upcoming"`
	// Record is the member's record, with its text, photos and comments.
	Record *Record `json:"record,omitempty"`
}

// Timeline lists a baby's milestones, oldest first, with the next automatic
// one at the end.
func (s *Store) Timeline(ctx context.Context, actor Actor, babyID string) ([]TimelineEntry, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return nil, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return nil, err
	}
	birth, _ := parseDate(baby.BirthDate)
	today := s.today()
	rows, err := s.queryRows(ctx, s.db, "SELECT "+recordColumns+
		" FROM records WHERE baby_id = ? AND kind = 'milestone' AND deleted_at IS NULL ORDER BY occurred_on, created_at LIMIT 1000", babyID)
	if err != nil {
		return nil, err
	}
	records, err := s.present(ctx, s.db, actor, baby, rows)
	if err != nil {
		return nil, err
	}
	annotated := map[string]*Record{}
	entries := []TimelineEntry{}
	for i := range records {
		record := &records[i]
		if key := record.Milestone.AutoKey; key != "" {
			annotated[key] = record
			continue
		}
		date, _ := parseDate(record.OccurredOn)
		age, _ := ageOn(birth, date)
		entries = append(entries, TimelineEntry{Date: record.OccurredOn, Age: age, Title: record.Milestone.Title, Record: record})
	}
	for _, auto := range autoMilestones(birth, today) {
		age, _ := ageOn(birth, auto.Date)
		entries = append(entries, TimelineEntry{Date: auto.Date.Format(dateLayout), Age: age, Auto: auto.Key,
			Title: auto.Title, Upcoming: auto.Date.After(today), Record: annotated[auto.Key]})
	}
	slices.SortStableFunc(entries, func(a, b TimelineEntry) int {
		if a.Date != b.Date {
			return strings.Compare(a.Date, b.Date)
		}
		// On the same day, the automatic milestone comes first.
		switch {
		case a.Auto != "" && b.Auto == "":
			return -1
		case a.Auto == "" && b.Auto != "":
			return 1
		}
		return 0
	})
	return entries, nil
}
