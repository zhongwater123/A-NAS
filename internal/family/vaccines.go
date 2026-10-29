package family

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// CustomVaccine is the code of a dose that is not in the catalog; its name
// is whatever the member typed.
const CustomVaccine = "custom"

// soonDays is how far ahead a dose counts as coming up soon.
const soonDays = 30

//go:embed standards/vaccines.json
var vaccineCatalogJSON []byte

// VaccineCatalog is the schedule the family center ships: the national
// immunization programme's doses and the common non-programme vaccines a
// family may plan, with the source they were taken from.
type VaccineCatalog struct {
	Version  string           `json:"version"`
	Title    string           `json:"title"`
	Source   string           `json:"source"`
	Vaccines []CatalogVaccine `json:"vaccines"`
}

type CatalogVaccine struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Programme marks the national immunization programme's free vaccines;
	// the others are non-programme vaccines a family chooses.
	Programme bool `json:"programme"`
	// Covers lists the programme vaccines whose doses this vaccine stands in
	// for: its Nth dose counts as their Nth dose.
	Covers []string `json:"covers,omitempty"`
	// CoversUpTo limits Covers to the covered vaccines' first doses.
	CoversUpTo int `json:"coversUpTo,omitempty"`
	// Group names programme vaccines that are alternatives, of which each
	// region uses one, such as live or inactivated JE vaccine. The first in
	// the catalog is the default; a family picks another in its plan.
	Group string `json:"group,omitempty"`
	// Doses are empty for vaccines only recorded, never scheduled, such as
	// influenza, which is given every season.
	Doses []CatalogDose `json:"doses"`
	Note  string        `json:"note,omitempty"`
}

type CatalogDose struct {
	Dose int `json:"dose"`
	// AgeMonths is the recommended age; 0 is at birth.
	AgeMonths int    `json:"ageMonths"`
	AgeLabel  string `json:"ageLabel"`
	// Sex limits the dose to girls ("female") or boys ("male").
	Sex string `json:"sex,omitempty"`
}

var vaccineCatalog = mustCatalog(vaccineCatalogJSON)

func mustCatalog(raw []byte) VaccineCatalog {
	var catalog VaccineCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		panic(fmt.Sprintf("vaccine catalog: %v", err))
	}
	seen := map[string]bool{}
	for _, vaccine := range catalog.Vaccines {
		if vaccine.Code == "" || vaccine.Code == CustomVaccine || seen[vaccine.Code] || vaccine.Programme && len(vaccine.Doses) == 0 {
			panic("vaccine catalog: bad or repeated code " + vaccine.Code)
		}
		seen[vaccine.Code] = true
	}
	for _, vaccine := range catalog.Vaccines {
		for _, covered := range vaccine.Covers {
			if !seen[covered] {
				panic("vaccine catalog: " + vaccine.Code + " covers unknown " + covered)
			}
		}
	}
	return catalog
}

func vaccineByCode(code string) (CatalogVaccine, bool) {
	for _, vaccine := range vaccineCatalog.Vaccines {
		if vaccine.Code == code {
			return vaccine, true
		}
	}
	return CatalogVaccine{}, false
}

// DoseStatus is where a scheduled dose stands for a baby.
type DoseStatus string

const (
	DoseGiven    DoseStatus = "given"
	DoseReplaced DoseStatus = "replaced"
	// DoseAvailable is due: the recommended age has come.
	DoseAvailable DoseStatus = "available"
	DoseSoon      DoseStatus = "soon"
	DoseUpcoming  DoseStatus = "upcoming"
)

// ScheduledDose is one dose of a baby's schedule.
type ScheduledDose struct {
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Dose      int        `json:"dose"`
	Programme bool       `json:"programme"`
	AgeLabel  string     `json:"ageLabel"`
	DueDate   string     `json:"dueDate"`
	Status    DoseStatus `json:"status"`
	GivenOn   string     `json:"givenOn,omitempty"`
	RecordID  string     `json:"recordId,omitempty"`
	// ReplacedBy names the non-programme vaccine whose dose stood in.
	ReplacedBy string `json:"replacedBy,omitempty"`
}

// VaccineSchedule is a baby's schedule: the programme's doses and those of
// the vaccines the family plans, and every dose recorded.
type VaccineSchedule struct {
	Version string          `json:"version"`
	Title   string          `json:"title"`
	Source  string          `json:"source"`
	Doses   []ScheduledDose `json:"doses"`
	Plan    []string        `json:"plan"`
	Records []Record        `json:"records"`
}

// Vaccines works out a baby's schedule from the catalog and the records.
func (s *Store) Vaccines(ctx context.Context, actor Actor, babyID string) (VaccineSchedule, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return VaccineSchedule{}, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return VaccineSchedule{}, err
	}
	rows, err := s.queryRows(ctx, s.db, "SELECT "+recordColumns+
		" FROM records WHERE baby_id = ? AND kind = 'vaccine' AND deleted_at IS NULL ORDER BY occurred_on, created_at LIMIT 1000", babyID)
	if err != nil {
		return VaccineSchedule{}, err
	}
	records, err := s.present(ctx, s.db, actor, baby, rows)
	if err != nil {
		return VaccineSchedule{}, err
	}
	plan, err := s.vaccinePlan(ctx, s.db, babyID)
	if err != nil {
		return VaccineSchedule{}, err
	}
	birth, _ := parseDate(baby.BirthDate)
	return VaccineSchedule{
		Version: vaccineCatalog.Version, Title: vaccineCatalog.Title, Source: vaccineCatalog.Source,
		Doses: schedule(birth, baby.Sex, s.today(), plan, records), Plan: plan, Records: records,
	}, nil
}

func (s *Store) vaccinePlan(ctx context.Context, q queryer, babyID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT vaccine_code FROM vaccine_plans WHERE baby_id = ? ORDER BY vaccine_code", babyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plan := []string{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		plan = append(plan, code)
	}
	return plan, rows.Err()
}

// SetVaccinePlan chooses the non-programme vaccines whose doses a baby's
// schedule lists. Any member may plan them.
func (s *Store) SetVaccinePlan(ctx context.Context, actor Actor, babyID string, codes []string) (VaccineSchedule, error) {
	for _, code := range codes {
		vaccine, ok := vaccineByCode(code)
		if !ok || vaccine.Programme && vaccine.Group == "" {
			return VaccineSchedule{}, ErrInvalid
		}
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := s.baby(ctx, tx, babyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM vaccine_plans WHERE baby_id = ?", babyID); err != nil {
			return err
		}
		for _, code := range codes {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO vaccine_plans(baby_id, vaccine_code) VALUES(?, ?)", babyID, code); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor, "family.vaccine_plan_changed", babyID, "")
	})
	if err != nil {
		return VaccineSchedule{}, err
	}
	return s.Vaccines(ctx, actor, babyID)
}

// schedule lists the doses of the programme and of the planned vaccines,
// each with its status on today. A planned vaccine's dose given stands in
// for the same dose of each programme vaccine it covers.
func schedule(birth time.Time, sex string, today time.Time, plan []string, records []Record) []ScheduledDose {
	given := map[[2]string]Record{}
	for _, record := range records {
		if v := record.Vaccine; v != nil {
			key := [2]string{v.Code, fmt.Sprint(v.Dose)}
			if _, ok := given[key]; !ok {
				given[key] = record
			}
		}
	}
	soon := today.AddDate(0, 0, soonDays)
	chosen := chosenAlternatives(plan, given)
	doses := []ScheduledDose{}
	for _, vaccine := range vaccineCatalog.Vaccines {
		if !vaccine.Programme && !slices.Contains(plan, vaccine.Code) {
			continue
		}
		if vaccine.Group != "" && chosen[vaccine.Group] != vaccine.Code {
			continue
		}
		for _, dose := range vaccine.Doses {
			if dose.Sex != "" && dose.Sex != sex {
				continue
			}
			due := addMonths(birth, dose.AgeMonths)
			scheduled := ScheduledDose{Code: vaccine.Code, Name: vaccine.Name, Dose: dose.Dose, Programme: vaccine.Programme,
				AgeLabel: dose.AgeLabel, DueDate: due.Format(dateLayout)}
			switch {
			case recordFor(given, vaccine.Code, dose.Dose) != nil:
				record := recordFor(given, vaccine.Code, dose.Dose)
				scheduled.Status, scheduled.GivenOn, scheduled.RecordID = DoseGiven, record.OccurredOn, record.ID
			case vaccine.Programme && replacement(given, vaccine.Code, dose.Dose) != nil:
				record, by := replacement(given, vaccine.Code, dose.Dose), ""
				if other, ok := vaccineByCode(record.Vaccine.Code); ok {
					by = other.Name
				}
				scheduled.Status, scheduled.GivenOn, scheduled.RecordID, scheduled.ReplacedBy = DoseReplaced, record.OccurredOn, record.ID, by
			case !due.After(today):
				scheduled.Status = DoseAvailable
			case !due.After(soon):
				scheduled.Status = DoseSoon
			default:
				scheduled.Status = DoseUpcoming
			}
			doses = append(doses, scheduled)
		}
	}
	slices.SortStableFunc(doses, func(a, b ScheduledDose) int {
		if a.DueDate != b.DueDate {
			if a.DueDate < b.DueDate {
				return -1
			}
			return 1
		}
		return 0
	})
	return doses
}

func recordFor(given map[[2]string]Record, code string, dose int) *Record {
	if record, ok := given[[2]string{code, fmt.Sprint(dose)}]; ok {
		return &record
	}
	return nil
}

// replacement finds a recorded dose of a vaccine that covers code's dose.
func replacement(given map[[2]string]Record, code string, dose int) *Record {
	var found *Record
	for _, vaccine := range vaccineCatalog.Vaccines {
		if !slices.Contains(vaccine.Covers, code) || vaccine.CoversUpTo > 0 && dose > vaccine.CoversUpTo {
			continue
		}
		if record := recordFor(given, vaccine.Code, dose); record != nil && (found == nil || record.OccurredOn < found.OccurredOn) {
			found = record
		}
	}
	return found
}

// chosenAlternatives picks, for each group of alternative programme
// vaccines, the one the plan names, else one with a dose recorded, else the
// catalog's first.
func chosenAlternatives(plan []string, given map[[2]string]Record) map[string]string {
	chosen := map[string]string{}
	recorded := map[string]bool{}
	for key := range given {
		recorded[key[0]] = true
	}
	for _, rank := range []func(CatalogVaccine) bool{
		func(v CatalogVaccine) bool { return slices.Contains(plan, v.Code) },
		func(v CatalogVaccine) bool { return recorded[v.Code] },
		func(CatalogVaccine) bool { return true },
	} {
		for _, vaccine := range vaccineCatalog.Vaccines {
			if vaccine.Group != "" && chosen[vaccine.Group] == "" && rank(vaccine) {
				chosen[vaccine.Group] = vaccine.Code
			}
		}
	}
	return chosen
}
