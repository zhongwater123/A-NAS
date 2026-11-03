package websearch

import (
	"cmp"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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
	URL     string   `json:"url"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Engine  string   `json:"engine"`
	Engines []string `json:"engines"`
	// Positions are the result's ranks in the engines that found it, the
	// first in Engine's.
	Positions     []int   `json:"positions"`
	PublishedDate *string `json:"publishedDate"`
	Thumbnail     string  `json:"thumbnail"`
	ThumbnailSrc  string  `json:"thumbnail_src"`
	ImgSrc        string  `json:"img_src"`
	Resolution    string  `json:"resolution"`
	Source        string  `json:"source"`
	Author        string  `json:"author"`
	Length        any     `json:"length"`
	Seed          any     `json:"seed"`
	Leech         any     `json:"leech"`
	Filesize      any     `json:"filesize"`
	MagnetLink    string  `json:"magnetlink"`
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

// infobox is the first of SearXNG's infoboxes that has a title.
func (s *Service) infobox(raws []RawInfobox) *Infobox {
	for _, raw := range raws {
		title := strings.TrimSpace(raw.Infobox)
		if title == "" {
			continue
		}
		box := &Infobox{Title: title, Content: htmlText(raw.Content), Thumbnail: s.thumbnail(raw.ImgSrc)}
		for _, link := range append([]struct {
			URL string `json:"url"`
		}{{URL: raw.ID}}, raw.URLs...) {
			if webAddress(link.URL) {
				box.URL = link.URL
				break
			}
		}
		return box
	}
	return nil
}

// htmlText is an infobox's content as the window shows it: its words, a line
// for each paragraph or item. SearXNG's page shows an infobox's content as
// HTML (simple/elements/infobox.html), and dictionary engines such as Jisho
// write paragraphs, lists and links in it.
func htmlText(content string) string {
	var lines []string
	var line strings.Builder
	end := func() {
		if text := strings.Join(strings.Fields(line.String()), " "); text != "" {
			lines = append(lines, text)
		}
		line.Reset()
	}
	tokens := html.NewTokenizer(strings.NewReader(content))
	hidden := false
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			end()
			return strings.Join(lines, "\n")
		case html.TextToken:
			if !hidden {
				line.Write(tokens.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			tag := atom.Lookup(name)
			if tag == atom.Script || tag == atom.Style {
				hidden = kind == html.StartTagToken
			}
			if lineTags[tag] {
				end()
			}
		}
	}
}

// lineTags start or end a line of an infobox's content.
var lineTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.Div: true, atom.Li: true, atom.Ul: true, atom.Ol: true, atom.Dt: true, atom.Dd: true,
	atom.Tr: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Hr: true,
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
	// The engine whose turn the result takes comes first; SearXNG lists the
	// others in no particular order.
	names := raw.Engines
	if raw.Engine != "" {
		names = append([]string{raw.Engine}, slices.DeleteFunc(slices.Clone(raw.Engines), func(n string) bool { return n == raw.Engine })...)
	}
	for _, name := range names {
		if l := label(name); !slices.Contains(result.Engines, l) {
			result.Engines = append(result.Engines, l)
			if platforms[name] && result.Platform == "" {
				result.Platform = l
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

// interleave lets the tab's engines take turns, A→B→C→A, each with its own
// results in its own order. SearXNG ranks a result by its place in each
// engine and then groups results that share a layout, which put one engine's
// results ahead of all the others'. A result several engines found takes its
// first engine's turn; an engine missing from order goes after the others.
func interleave(results []RawResult, order []string) []RawResult {
	byEngine := map[string][]RawResult{}
	turns := slices.Clone(order)
	for _, raw := range results {
		name := raw.Engine
		if name == "" && len(raw.Engines) > 0 {
			name = raw.Engines[0]
		}
		if !slices.Contains(turns, name) {
			turns = append(turns, name)
		}
		byEngine[name] = append(byEngine[name], raw)
	}
	for _, list := range byEngine {
		slices.SortStableFunc(list, func(a, b RawResult) int { return cmp.Compare(place(a), place(b)) })
	}
	ordered := make([]RawResult, 0, len(results))
	for len(ordered) < len(results) {
		for _, name := range turns {
			if list := byEngine[name]; len(list) > 0 {
				ordered = append(ordered, list[0])
				byEngine[name] = list[1:]
			}
		}
	}
	return ordered
}

// place is a result's rank in its first engine; one without goes last.
func place(raw RawResult) int {
	if len(raw.Positions) > 0 && raw.Positions[0] > 0 {
		return raw.Positions[0]
	}
	return math.MaxInt
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

// localLayouts are the dates SearXNG writes without a zone: its engines make
// them from Chinese sites' times or from timestamps in SearXNG's own zone,
// which is this machine's, as the Product Service's is.
var localLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02"}

func publishedAt(value *string) *time.Time {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(*value)
	if t, err := time.Parse(time.RFC3339Nano, text); err == nil && t.Year() > 1970 {
		return &t
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil && t.Year() > 1970 {
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

// sizeText is a size as SearXNG's engines write it: a number, perhaps a
// space, and a unit.
var sizeText = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(?:([KMGT])I?)?B?$`)

// size reads bytes or a size such as "885.68 MB". SearXNG writes sizes with
// searx.utils.humanize_bytes, which counts in powers of 1024 but labels them
// KB, MB and GB, so every prefix here is binary; a parser of decimal units
// (go-humanize's ParseBytes) would read these 5 % short.
func size(value any) *int64 {
	var bytes float64
	switch v := value.(type) {
	case float64:
		bytes = v
	case string:
		match := sizeText.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(v)))
		if match == nil {
			return nil
		}
		n, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return nil
		}
		shift := map[string]int{"": 0, "K": 10, "M": 20, "G": 30, "T": 40}[match[2]]
		bytes = n * float64(int64(1)<<shift)
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
