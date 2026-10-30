package websearch

import (
	"encoding/json"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Response is the part of SearXNG's JSON answer A-NAS reads
// (searx/webutils.py, searx/result_types). Engines fill a result's fields
// unevenly: numbers arrive as strings and missing values as null or "".
type Response struct {
	Results     []RawResult       `json:"results"`
	Answers     []json.RawMessage `json:"answers"`
	Infoboxes   []RawInfobox      `json:"infoboxes"`
	Suggestions []string          `json:"suggestions"`
	// Unresponsive pairs an engine's name with why it gave no results.
	Unresponsive [][]string `json:"unresponsive_engines"`
}

// RawResult is one SearXNG result as the engine filled it.
type RawResult struct {
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Engine        string   `json:"engine"`
	Engines       []string `json:"engines"`
	PublishedDate *string  `json:"publishedDate"`
	Thumbnail     string   `json:"thumbnail"`
	ThumbnailSrc  string   `json:"thumbnail_src"`
	ImgSrc        string   `json:"img_src"`
	Resolution    string   `json:"resolution"`
	Source        string   `json:"source"`
	Author        string   `json:"author"`
	Length        any      `json:"length"`
	Seed          any      `json:"seed"`
	Leech         any      `json:"leech"`
	Filesize      any      `json:"filesize"`
	MagnetLink    string   `json:"magnetlink"`
}

// RawInfobox is a SearXNG infobox, such as an encyclopedia's summary.
type RawInfobox struct {
	Infobox string `json:"infobox"`
	Content string `json:"content"`
	ImgSrc  string `json:"img_src"`
	ID      string `json:"id"`
	URLs    []struct {
		URL string `json:"url"`
	} `json:"urls"`
}

// Page is one page of a tab's results, as the window shows them.
type Page struct {
	Results     []Result `json:"results"`
	Answers     []Answer `json:"answers"`
	Infobox     *Infobox `json:"infobox,omitempty"`
	Suggestions []string `json:"suggestions"`
	// Unresponsive names the tab's engines that gave no results this time.
	Unresponsive []string `json:"unresponsive"`
}

// Result is one result. Fields that do not apply to the tab are empty.
type Result struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content,omitempty"`
	Domain  string `json:"domain"`
	// Engines name the engines that found it.
	Engines     []string   `json:"engines"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	// Thumbnail is a path of this API, never another host.
	Thumbnail string `json:"thumbnail,omitempty"`
	// Platform is the site a video plays on.
	Platform        string `json:"platform,omitempty"`
	Author          string `json:"author,omitempty"`
	DurationSeconds *int   `json:"durationSeconds,omitempty"`
	// Width and Height are an image's, and Source where it appears.
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Source string `json:"source,omitempty"`
	// A torrent's link, size and peers.
	Magnet    string `json:"magnet,omitempty"`
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
	Seeders   *int   `json:"seeders,omitempty"`
	Leechers  *int   `json:"leechers,omitempty"`
}

// Answer is a direct answer, such as a calculation.
type Answer struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Infobox is a summary shown above the results.
type Infobox struct {
	Title     string `json:"title"`
	Content   string `json:"content,omitempty"`
	URL       string `json:"url,omitempty"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

func (s *Service) page(category Category, response Response) Page {
	page := Page{Results: []Result{}, Answers: []Answer{}, Suggestions: []string{}, Unresponsive: []string{}}
	for _, raw := range response.Results {
		if result, ok := s.result(category, raw); ok {
			page.Results = append(page.Results, result)
		}
	}
	for _, raw := range response.Answers {
		if answer, ok := decodeAnswer(raw); ok {
			page.Answers = append(page.Answers, answer)
		}
	}
	for _, raw := range response.Infoboxes {
		if title := strings.TrimSpace(raw.Infobox); title != "" {
			box := &Infobox{Title: title, Content: strings.TrimSpace(raw.Content), Thumbnail: s.thumbnail(raw.ImgSrc)}
			for _, link := range append([]struct {
				URL string `json:"url"`
			}{{URL: raw.ID}}, raw.URLs...) {
				if webAddress(link.URL) {
					box.URL = link.URL
					break
				}
			}
			page.Infobox = box
			break
		}
	}
	for _, suggestion := range response.Suggestions {
		if suggestion = strings.TrimSpace(suggestion); suggestion != "" {
			page.Suggestions = append(page.Suggestions, suggestion)
		}
	}
	for _, pair := range response.Unresponsive {
		if len(pair) == 0 {
			continue
		}
		if e, ok := lookupEngine(pair[0]); ok && !slices.Contains(page.Unresponsive, e.label) {
			page.Unresponsive = append(page.Unresponsive, e.label)
		}
	}
	return page
}

func (s *Service) result(category Category, raw RawResult) (Result, bool) {
	if !webAddress(raw.URL) {
		return Result{}, false
	}
	parsed, _ := url.Parse(raw.URL)
	result := Result{
		URL: raw.URL, Title: strings.TrimSpace(raw.Title), Content: strings.TrimSpace(raw.Content),
		Domain: strings.TrimPrefix(parsed.Hostname(), "www."), Engines: []string{},
		PublishedAt: publishedAt(raw.PublishedDate), Author: strings.TrimSpace(raw.Author),
	}
	if result.Title == "" {
		result.Title = result.Domain
	}
	// Some engines put a dash where they have no summary.
	if result.Content == "-" {
		result.Content = ""
	}
	names := raw.Engines
	if len(names) == 0 && raw.Engine != "" {
		names = []string{raw.Engine}
	}
	for _, name := range names {
		if e, ok := lookupEngine(name); ok && !slices.Contains(result.Engines, e.label) {
			result.Engines = append(result.Engines, e.label)
			if e.platform && result.Platform == "" {
				result.Platform = e.label
			}
		}
	}
	switch category {
	case Images:
		result.Thumbnail = s.thumbnail(firstNonEmpty(raw.ThumbnailSrc, raw.ImgSrc, raw.Thumbnail))
		result.Width, result.Height = resolution(raw.Resolution)
		result.Source = strings.TrimSpace(raw.Source)
	case Videos:
		result.Thumbnail = s.thumbnail(firstNonEmpty(raw.Thumbnail, raw.ThumbnailSrc, raw.ImgSrc))
		result.DurationSeconds = duration(raw.Length)
		if result.Platform == "" {
			result.Platform = result.Domain
		}
	case Torrents:
		if strings.HasPrefix(raw.MagnetLink, "magnet:?") {
			result.Magnet = raw.MagnetLink
		}
		result.SizeBytes = size(raw.Filesize)
		result.Seeders, result.Leechers = count(raw.Seed), count(raw.Leech)
	default:
		result.Thumbnail = s.thumbnail(firstNonEmpty(raw.Thumbnail, raw.ImgSrc))
	}
	return result, true
}

func decodeAnswer(raw json.RawMessage) (Answer, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		return Answer{Text: text}, text != ""
	}
	var object struct {
		Answer string `json:"answer"`
		URL    string `json:"url"`
	}
	if json.Unmarshal(raw, &object) != nil || strings.TrimSpace(object.Answer) == "" {
		return Answer{}, false
	}
	answer := Answer{Text: strings.TrimSpace(object.Answer)}
	if webAddress(object.URL) {
		answer.URL = object.URL
	}
	return answer, true
}

// webAddress is an absolute http or https address, the only kind the window
// links to.
func webAddress(address string) bool {
	parsed, err := url.Parse(address)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// absoluteImage completes a scheme-relative address, as Bilibili gives, and
// keeps only web addresses.
func absoluteImage(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(address, "//") {
		address = "https:" + address
	}
	if !webAddress(address) {
		return ""
	}
	return address
}

var publishedLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func publishedAt(value *string) *time.Time {
	if value == nil {
		return nil
	}
	for _, layout := range publishedLayouts {
		if t, err := time.Parse(layout, strings.TrimSpace(*value)); err == nil && t.Year() > 1970 {
			return &t
		}
	}
	return nil
}

// resolution reads "923 x 880".
func resolution(value string) (int, int) {
	width, height, ok := strings.Cut(strings.ReplaceAll(value, " ", ""), "x")
	if !ok {
		return 0, 0
	}
	w, errW := strconv.Atoi(width)
	h, errH := strconv.Atoi(height)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

// duration reads seconds ("233.0" or 233) or a clock ("2:16:29").
func duration(value any) *int {
	switch v := value.(type) {
	case float64:
		return seconds(v)
	case string:
		v = strings.TrimSpace(v)
		if number, err := strconv.ParseFloat(v, 64); err == nil {
			return seconds(number)
		}
		total := 0
		parts := strings.Split(v, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil
		}
		for _, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return nil
			}
			total = total*60 + n
		}
		return &total
	}
	return nil
}

func seconds(value float64) *int {
	if value <= 0 || value > 100*24*3600 || math.IsNaN(value) {
		return nil
	}
	n := int(math.Round(value))
	return &n
}

// size reads bytes or a size such as "885.68 MB"; the window shows it in its
// own units, so binary and decimal prefixes count the same.
func size(value any) *int64 {
	var bytes float64
	switch v := value.(type) {
	case float64:
		bytes = v
	case string:
		number, unit, _ := strings.Cut(strings.TrimSpace(v), " ")
		n, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return nil
		}
		multiplier := map[string]float64{
			"": 1, "B": 1, "KB": 1 << 10, "KIB": 1 << 10, "MB": 1 << 20, "MIB": 1 << 20,
			"GB": 1 << 30, "GIB": 1 << 30, "TB": 1 << 40, "TIB": 1 << 40,
		}[strings.ToUpper(strings.TrimSpace(unit))]
		if multiplier == 0 {
			return nil
		}
		bytes = n * multiplier
	default:
		return nil
	}
	if bytes <= 0 || bytes > 1<<60 {
		return nil
	}
	n := int64(bytes)
	return &n
}

func count(value any) *int {
	var n int
	switch v := value.(type) {
	case float64:
		n = int(v)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil
		}
		n = parsed
	default:
		return nil
	}
	if n < 0 {
		return nil
	}
	return &n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
