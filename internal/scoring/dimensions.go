package scoring

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Dimension is something raters score a photo on, a 打分项: one of the
// built-in ones, or one an administrator made, which every later batch can
// choose too. Description tells raters what to look at; the scoring
// handbook's built-in ones have one, the three older ones none.
type Dimension struct {
	ID          string
	Name        string
	Description string
	Custom      bool
}

// Overall is 总体, which was named 总分 before.
const Overall = "overall"

// Sharpness is 清晰度.
const Sharpness = "sharpness"

// Emotion is 情绪, which describes a photo rather than judges it: three and
// five levels name it from calm to intense (scales.go).
const Emotion = "emotion"

// lowestEmotion is 1.00, the calmest 情绪 on every scale.
const lowestEmotion = 100

// BuiltIn lists the 打分项 every module has, in the order a new batch offers
// them: the scoring handbook's five, then the older three. Their IDs are
// stored and exported, so an ID never changes meaning; a new one is added,
// not renamed, though a name may change.
var BuiltIn = []Dimension{
	{ID: Overall, Name: "总体", Description: "整体拍得好不好，有没有让人想多看一会儿的感觉"},
	{ID: "composition", Name: "构图", Description: "画面安排得好不好，主体清不清楚"},
	{ID: "light_color", Name: "光影色彩", Description: "光线和颜色好不好看"},
	{ID: "quality", Name: "画质", Description: "按正常大小看有没有抖动、虚焦、噪点，不用 100% 查看"},
	{ID: Emotion, Name: "情绪", Description: "平静还是激烈，只看感觉不看好坏：1 平静，5 激烈"},
	{ID: Sharpness, Name: "清晰度"},
	{ID: "narrative", Name: "叙事意境"},
	{ID: "preference", Name: "喜好程度"},
}

// formerNames are names built-in 打分项 had before. Older batches, exports
// and raters still know overall as 总分, so no custom 打分项 takes it.
var formerNames = []string{"总分"}

const (
	// MaxBatchDimensions is how many 打分项 a batch scores at most.
	MaxBatchDimensions = 8
	// A custom 打分项's name and description, in characters.
	maxDimensionNameRunes        = 8
	maxDimensionDescriptionRunes = 40
	// maxCustomDimensions bounds what administrators can make.
	maxCustomDimensions = 200
	// customPrefix starts a custom 打分项's ID, which is followed by its
	// number: custom_1, custom_2 and so on, never reused.
	customPrefix = "custom_"
)

// Catalogue is every 打分项 a batch can choose: the built-in ones, then the
// custom ones, oldest first.
type Catalogue []Dimension

// Catalogue reads every 打分项 a batch can choose.
func (s *Store) Catalogue(ctx context.Context) (Catalogue, error) {
	return readCatalogue(ctx, s.db)
}

func readCatalogue(ctx context.Context, db querier) (Catalogue, error) {
	catalogue := Catalogue(slices.Clone(BuiltIn))
	err := each(ctx, db, "SELECT id, name, description FROM custom_dimensions ORDER BY number", nil, func(rows *sql.Rows) error {
		dimension := Dimension{Custom: true}
		if err := rows.Scan(&dimension.ID, &dimension.Name, &dimension.Description); err != nil {
			return err
		}
		catalogue = append(catalogue, dimension)
		return nil
	})
	return catalogue, err
}

func (c Catalogue) find(id string) (Dimension, bool) {
	index := slices.IndexFunc(c, func(dimension Dimension) bool { return dimension.ID == id })
	if index < 0 {
		return Dimension{}, false
	}
	return c[index], true
}

// Describe returns the 打分项 of ids in their order; an ID the catalogue
// does not know keeps the ID as its name.
func (c Catalogue) Describe(ids []string) []Dimension {
	described := make([]Dimension, len(ids))
	for i, id := range ids {
		dimension, ok := c.find(id)
		if !ok {
			dimension = Dimension{ID: id, Name: id}
		}
		described[i] = dimension
	}
	return described
}

// check makes sure a batch's choice is 1 to MaxBatchDimensions different 打分项
// of the catalogue. Raters score them in the order given, and a rating keeps
// the first in ratings.score.
func (c Catalogue) check(ids []string) error {
	switch {
	case len(ids) == 0:
		return invalid("choose at least one scoring dimension")
	case len(ids) > MaxBatchDimensions:
		return invalid("a batch scores at most %d dimensions", MaxBatchDimensions)
	}
	chosen := map[string]bool{}
	for _, id := range ids {
		switch _, known := c.find(id); {
		case !known:
			return invalid("%q is not a scoring dimension", id)
		case chosen[id]:
			return invalid("%q is chosen twice", id)
		}
		chosen[id] = true
	}
	return nil
}

// CreateDimension makes a custom 打分项 that every batch from now on can
// choose. Its name is unique among them all, and none a built-in one had
// before (formerNames).
func (s *Store) CreateDimension(ctx context.Context, by, name, description string) (Dimension, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	switch {
	case name == "" || utf8.RuneCountInString(name) > maxDimensionNameRunes || !printable(name):
		return Dimension{}, invalid("a name has 1 to %d characters", maxDimensionNameRunes)
	case utf8.RuneCountInString(description) > maxDimensionDescriptionRunes || !printable(description):
		return Dimension{}, invalid("a description has at most %d characters on one line", maxDimensionDescriptionRunes)
	}
	dimension := Dimension{Name: name, Description: description, Custom: true}
	err := s.within(ctx, func(tx *sql.Tx) error {
		catalogue, err := readCatalogue(ctx, tx)
		if err != nil {
			return err
		}
		if slices.Contains(formerNames, name) || slices.ContainsFunc(catalogue, func(other Dimension) bool { return other.Name == name }) {
			return ErrNameTaken
		}
		if len(catalogue)-len(BuiltIn) >= maxCustomDimensions {
			return invalid("there are %d custom scoring dimensions already", maxCustomDimensions)
		}
		var last int
		err = tx.QueryRowContext(ctx, "SELECT coalesce(max(number), 0) FROM custom_dimensions").Scan(&last)
		if err != nil {
			return err
		}
		dimension.ID = customPrefix + strconv.Itoa(last+1)
		_, err = tx.ExecContext(ctx, `INSERT INTO custom_dimensions (id, number, name, description, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, dimension.ID, last+1, name, description, by, s.timestamp())
		return err
	})
	if err != nil {
		return Dimension{}, err
	}
	return dimension, nil
}

// printable rejects line breaks and other control characters.
func printable(text string) bool {
	return !strings.ContainsFunc(text, unicode.IsControl)
}

// The batches table keeps a batch's dimensions as their IDs joined by commas,
// in the order raters score them.
func joinDimensions(ids []string) string { return strings.Join(ids, ",") }

func splitDimensions(stored string) []string {
	if stored == "" {
		return []string{Overall}
	}
	return strings.Split(stored, ",")
}

// laterDimensions are a batch's dimensions after the first, which ratings
// keep in rating_scores; the first is in ratings.score.
func laterDimensions(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids[1:]
}
