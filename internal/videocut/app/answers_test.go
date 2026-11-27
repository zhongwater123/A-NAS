package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The outline's schema gives each part its own limit of pictures, by
// prefixItems (ai/anas_videocut/algo/plan.py): a picture past its music
// part's slots is never filled, and the job failed at rendering once the
// round had been spent. The checker has to honour prefixItems for that.
func TestAnOutlineWithMorePicturesThanAPartHasSlotsIsRefused(t *testing.T) {
	part := func(slots int) string {
		return fmt.Sprintf(`{"type":"object","required":["scenes"],"properties":{"scenes":{"type":"array","minItems":1,"maxItems":%d}}}`, slots)
	}
	schema := json.RawMessage(`{"type":"object","required":["parts"],"properties":{"parts":{"type":"array","minItems":2,"maxItems":2,` +
		`"prefixItems":[` + part(2) + `,` + part(8) + `],"items":false}}}`)
	scenes := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat(`{"query":"海"},`, n), ",") + "]" }
	answer := func(first, second int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"parts":[{"scenes":%s},{"scenes":%s}]}`, scenes(first), scenes(second)))
	}
	if reasons := checkAnswer(schema, answer(2, 8), StepOutline); len(reasons) != 0 {
		t.Fatalf("an outline within each part's slots = %v", reasons)
	}
	reasons := checkAnswer(schema, answer(3, 2), StepOutline)
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "\n"), "/parts/0/scenes") {
		t.Fatalf("three pictures in a part of two slots = %v", reasons)
	}
}

// The limits of an outline's text are counted in characters, as the prompt
// says them (plan-v2.md): 2 KiB of bytes was about 680 Chinese characters,
// less than an outline of four parts takes. Card names are not text.
func TestAnOutlinesTextIsCountedInCharactersWithoutCardNames(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	field := strings.Repeat("海", maxAnswerRunes)
	parts := make([]string, 12)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"summary":%q,"cards":["%s"]}`, field, strings.Repeat("v", 100))
	}
	within := json.RawMessage(`{"parts":[` + strings.Join(parts, ",") + `]}`)
	if reasons := checkAnswer(schema, within, StepOutline); len(reasons) != 0 {
		t.Fatalf("12 fields of %d characters (%d in all), long card names = %v", maxAnswerRunes, 12*maxAnswerRunes, reasons)
	}
	over := json.RawMessage(`{"parts":[` + strings.Join(append(parts, fmt.Sprintf(`{"summary":%q}`, field)), ",") + `]}`)
	if reasons := checkAnswer(schema, over, StepOutline); len(reasons) != 1 || !strings.Contains(reasons[0], "超过 1500 字") {
		t.Fatalf("13 fields of %d characters = %v", maxAnswerRunes, reasons)
	}
	if reasons := checkAnswer(schema, json.RawMessage(fmt.Sprintf(`{"design":%q}`, field+"海")), StepOutline); len(reasons) != 1 {
		t.Fatalf("a field of %d characters = %v", maxAnswerRunes+1, reasons)
	}
}
