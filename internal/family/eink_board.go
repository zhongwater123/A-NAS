package family

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The board is what a shared e-ink screen shows of the family's to-dos
// (docs/specs/eink-screens.md, “看板：待办”). It reads to-dos and changes
// none; the screen outlet draws it and decides when to refresh.
const (
	// BoardRows is how many to-dos a screen lists; the rest are counted.
	BoardRows = 3
	// boardUrgentDays marks rows due within this many days, or overdue.
	boardUrgentDays = 3
	// boardMedical stands in for a medical to-do's title: the screen hangs
	// where the whole family and visitors see it.
	boardMedical = "一件医疗的事"
	// BoardEmpty is written when nothing is listed.
	BoardEmpty = "都处理完了"
)

// The companion's poses on a screen: asleep with nothing to do, waiting
// while everything fits, buried under leaves once rows are left out.
const (
	PetSleep  = "sleep"
	PetIdle   = "idle"
	PetBuried = "buried"
)

// boardLeadDays is how many days before its due date a to-do appears.
// Documents and contracts take longer to renew, so they show up earlier.
var boardLeadDays = map[string]int{"id_document": 60, "contract": 45}

const boardDefaultLeadDays = 30

// BoardRow is one listed to-do: what to do and how long is left.
type BoardRow struct {
	Text   string `json:"text"`
	When   string `json:"when"`
	Urgent bool   `json:"urgent"`
}

// Board is the to-do part of a shared screen.
type Board struct {
	Rows []BoardRow `json:"rows"`
	// More counts the to-dos that did not fit.
	More int    `json:"more"`
	Pet  string `json:"pet"`
}

// TodoBoard picks what a shared screen shows on today: open shared to-dos
// with a due date close enough, not snoozed past today, the most pressing
// first. Private to-dos never reach a shared screen.
func TodoBoard(todos []Todo, today time.Time) Board {
	type listed struct {
		todo Todo
		days int
	}
	var picked []listed
	for _, todo := range todos {
		if todo.Scope != TodoShared || todo.Status != TodoOpen || todo.Due == "" {
			continue
		}
		if until, ok := parseDate(todo.SnoozedUntil); ok && until.After(today) {
			continue
		}
		due, ok := parseDate(todo.Due)
		if !ok {
			continue
		}
		days := int(due.Sub(today).Hours() / 24)
		lead, ok := boardLeadDays[todo.Category]
		if !ok {
			lead = boardDefaultLeadDays
		}
		if days <= lead {
			picked = append(picked, listed{todo, days})
		}
	}
	slices.SortStableFunc(picked, func(a, b listed) int {
		return cmp.Or(cmp.Compare(a.days, b.days), cmp.Compare(a.todo.CreatedAt.UnixNano(), b.todo.CreatedAt.UnixNano()),
			strings.Compare(a.todo.ID, b.todo.ID))
	})
	board := Board{Rows: []BoardRow{}, Pet: PetSleep}
	for i, item := range picked {
		if i == BoardRows {
			board.More = len(picked) - BoardRows
			break
		}
		text := item.todo.Title
		if item.todo.Category == "medical" {
			text = boardMedical
		}
		board.Rows = append(board.Rows, BoardRow{Text: text, When: dueWords(item.days), Urgent: item.days <= boardUrgentDays})
	}
	switch {
	case board.More > 0:
		board.Pet = PetBuried
	case len(board.Rows) > 0:
		board.Pet = PetIdle
	}
	return board
}

// dueWords says how long is left, as the to-do window and reminders do.
func dueWords(days int) string {
	switch {
	case days < 0:
		return "已过期 " + strconv.Itoa(-days) + " 天"
	case days == 0:
		return "今天截止"
	}
	return "还有 " + strconv.Itoa(days) + " 天"
}

// Key names what the board says: the same words give the same key, across
// restarts, so a screen is not redrawn when nothing it shows has changed.
// The time of the update is left out on purpose.
func (b Board) Key() string {
	var text strings.Builder
	text.WriteString("todo\n")
	for _, row := range b.Rows {
		text.WriteString(row.Text + "\x1f" + row.When + "\x1f" + strconv.FormatBool(row.Urgent) + "\n")
	}
	text.WriteString(strconv.Itoa(b.More) + "\n" + b.Pet)
	sum := sha256.Sum256([]byte(text.String()))
	return hex.EncodeToString(sum[:8])
}
