package photosapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/binary"
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
	analysis := photos.GrantAnalysis{ObjectID: "video", Windows: photos.AnalysisDone, Described: photos.AnalysisDone, DescriptionsIdentity: "reader", WindowCount: 1, Dimensions: 3}
	for i := range 200 {
		analysis.Descriptions = append(analysis.Descriptions, photos.CardDescription{Card: i, Description: description})
	}
	vectors := bytes.Repeat([]byte{7}, analysis.Dimensions*4)
	windows, err := os.CreateTemp(t.TempDir(), "windows")
	if err != nil { t.Fatal(err) }
	defer windows.Close()
	if _, err := windows.Write(vectors); err != nil { t.Fatal(err) }
	if err := binary.Write(windows, binary.LittleEndian, [3]uint32{0,2000,0}); err != nil { t.Fatal(err) }
	file, err := os.Create(filepath.Join(t.TempDir(), "analysis"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	response, err := WriteAnalysis(file, analysis, windows)
	if err != nil {
		t.Fatal(err)
	}
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
