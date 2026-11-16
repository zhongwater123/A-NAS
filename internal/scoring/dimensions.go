package scoring

import (
	"slices"
	"strings"
)

// Dimension is something raters score a photo on, a 打分项.
type Dimension struct {
	ID   string
	Name string
}

// Overall is 总分: every batch scores it first, and a rating keeps it in its
// own score, so that batches of overall alone read as they always did.
const Overall = "overall"

// Sharpness is the 打分项 that lets raters see a photo at its full size.
const Sharpness = "sharpness"

// Dimensions lists every 打分项 in the order raters score them. Their IDs
// are stored and exported, so an ID never changes meaning; a new one is
// added, not renamed.
var Dimensions = []Dimension{
	{Overall, "总分"},
	{"composition", "构图"},
	{"light_color", "光影色彩"},
	{Sharpness, "清晰度"},
	{"narrative", "叙事意境"},
	{"preference", "喜好程度"},
}

// DimensionName is the name raters see for id, or id itself.
func DimensionName(id string) string {
	for _, dimension := range Dimensions {
		if dimension.ID == id {
			return dimension.Name
		}
	}
	return id
}

func knownDimension(id string) bool {
	return slices.ContainsFunc(Dimensions, func(dimension Dimension) bool { return dimension.ID == id })
}

// normalizeDimensions checks a batch's choice and puts it in the catalogue's
// order; no choice is overall alone.
func normalizeDimensions(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{Overall}, nil
	}
	chosen := map[string]bool{}
	for _, id := range ids {
		switch {
		case !knownDimension(id):
			return nil, invalid("%q is not a scoring dimension", id)
		case chosen[id]:
			return nil, invalid("%q is chosen twice", id)
		}
		chosen[id] = true
	}
	if !chosen[Overall] {
		return nil, invalid("every batch scores overall")
	}
	ordered := make([]string, 0, len(ids))
	for _, dimension := range Dimensions {
		if chosen[dimension.ID] {
			ordered = append(ordered, dimension.ID)
		}
	}
	return ordered, nil
}

// The batches table keeps a batch's dimensions as their IDs joined by commas.
func joinDimensions(ids []string) string { return strings.Join(ids, ",") }

func splitDimensions(stored string) []string {
	if stored == "" {
		return []string{Overall}
	}
	return strings.Split(stored, ",")
}

// extraDimensions are a batch's dimensions other than overall, which ratings
// keep in rating_scores.
func extraDimensions(ids []string) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == Overall })
}
