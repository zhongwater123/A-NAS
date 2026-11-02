// Package textroute picks the photos whose text local AI reads
// (docs/architecture/photo-ocr.md#哪些照片读文字). Reading one costs the NAS
// about two minutes, so only photos that look like documents, receipts, cards
// or screenshots are read: those whose vector, which every photo already has,
// comes close enough to one of the route's search queries. The queries and
// threshold are calibrated for one model on public datasets by ai/eval/ocr_route.py
// (docs/research/photo-ocr-routing.md), which writes route.json.
package textroute

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed route.json
var routeV1 []byte

// Route sends a photo to be read when its vector's similarity to any query's
// vector reaches Threshold.
type Route struct {
	// Model is the model the threshold was calibrated for; with any other
	// model no photo is routed.
	Model string
	// Queries are encoded as search queries are.
	Queries   []string
	Threshold float64
}

// V1 returns the route built into this program.
func V1() (Route, error) { return Parse(routeV1) }

// Parse reads a route written by ai/eval/ocr_route.py.
func Parse(data []byte) (Route, error) {
	var r struct {
		Model     string   `json:"model"`
		Queries   []string `json:"queries"`
		Threshold float64  `json:"threshold"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Route{}, fmt.Errorf("text route: %w", err)
	}
	if r.Model == "" || len(r.Queries) == 0 || r.Threshold <= -1 || r.Threshold >= 1 {
		return Route{}, fmt.Errorf("text route needs a model, queries and a threshold between -1 and 1")
	}
	seen := make(map[string]bool, len(r.Queries))
	for _, query := range r.Queries {
		if query == "" || seen[query] {
			return Route{}, fmt.Errorf("text route query %q is empty or repeated", query)
		}
		seen[query] = true
	}
	return Route{Model: r.Model, Queries: r.Queries, Threshold: r.Threshold}, nil
}
