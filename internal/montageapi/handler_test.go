package montageapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

// The largest request the contract allows fits the body limit: 413 is
// never the answer to a valid request.
func TestTheLargestRequestFits(t *testing.T) {
	ids := make([]string, montage.MaxAssets)
	for i := range ids {
		ids[i] = fmt.Sprintf("%0*d", montage.MaxIDLength, i)
	}
	body, err := json.Marshal(createRequest{
		RequestKey: strings.Repeat("~", montage.MaxRequestKey),
		Name:       strings.Repeat("\u2028", montage.MaxNameRunes), // escaped as \u2028
		AssetIDs:   ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxJSONBytes {
		t.Fatalf("the largest request is %d bytes; the limit is %d", len(body), maxJSONBytes)
	}
}
