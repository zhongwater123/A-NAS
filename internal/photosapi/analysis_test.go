package photosapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestLargeGrantAnalysisFitsSocketFrame(t *testing.T) {
	description, err := json.Marshal(map[string]string{"summary": strings.Repeat("雪", 512)})
	if err != nil {
		t.Fatal(err)
	}
	const count = 200
	analysis := photos.GrantAnalysis{ObjectID: "video", Windows: photos.AnalysisDone, Described: photos.AnalysisDone, DescriptionsIdentity: "reader", WindowCount: count, Dimensions: 1, CardCount: count, DescriptionCount: count}
	var cards []photos.CardAnalysis
	var headers [][3]uint32
	for i := range count {
		cards = append(cards, photos.CardAnalysis{Index: i, Description: &photos.CardDescription{Card: i, Description: description}})
		headers = append(headers, [3]uint32{uint32(i), uint32(i + 1), 0})
	}
	vectors := bytes.Repeat([]byte{7}, count*4)
	file, err := os.Create(filepath.Join(t.TempDir(), "analysis"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err = file.Write(vectors); err != nil {
		t.Fatal(err)
	}
	if err = binary.Write(file, binary.LittleEndian, headers); err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(file).Encode(cards); err != nil {
		t.Fatal(err)
	}
	response := GrantResponse{Analysis: &analysis, OK: true}
	envelope, err := json.Marshal(response)
	if err != nil || len(envelope) >= GrantMessageLimit {
		t.Fatalf("socket frame size %d, %v", len(envelope), err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= GrantMessageLimit {
		t.Fatal("bulk analysis should exceed the frame limit")
	}
	grants := Grants{Call: func(context.Context, GrantRequest) (GrantResponse, *os.File, error) { return response, file, nil }}
	got, raw, err := grants.Analysis(t.Context(), "grant", "asset")
	if err != nil || got.DescriptionsIdentity != "reader" || len(got.Descriptions) != 200 || !bytes.Equal(got.Descriptions[199].Description, description) || !bytes.Equal(raw, vectors) {
		t.Fatalf("read analysis: %d descriptions, %d vector bytes, %v", len(got.Descriptions), len(raw), err)
	}
}
