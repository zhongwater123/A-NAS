package workspace

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// AnchorAttribute marks each element written in a page's source with the
// byte offset of its start tag's "<" in that version, so that what a member
// picks in the sandboxed frame names a place in the source
// (docs/architecture/shared-workspace.md, 源码位置).
const AnchorAttribute = "data-anas-at"

// voidElements have no end tag.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

// Annotate returns src with AnchorAttribute on every start tag and helper
// inserted after the doctype, or before the first tag when there is none.
// Every other byte is src's own.
func Annotate(src []byte, helper string) []byte {
	var out bytes.Buffer
	out.Grow(len(src) + len(src)/8 + len(helper))
	inserted := false
	insert := func() {
		if !inserted {
			out.WriteString(helper)
			inserted = true
		}
	}
	walk(src, func(token html.TokenType, raw []byte, offset int, name string) {
		switch token {
		case html.DoctypeToken:
			out.Write(raw)
			insert()
		case html.StartTagToken, html.SelfClosingTagToken:
			insert()
			split := min(1+len(name), len(raw))
			out.Write(raw[:split])
			out.WriteString(" " + AnchorAttribute + `="` + strconv.Itoa(offset) + `"`)
			out.Write(raw[split:])
		default:
			out.Write(raw)
		}
	})
	insert()
	return out.Bytes()
}

// walk hands every token of src to visit with its raw bytes, its offset in
// src and, for tags, its lower-case name.
func walk(src []byte, visit func(token html.TokenType, raw []byte, offset int, name string)) {
	tokenizer := html.NewTokenizer(bytes.NewReader(src))
	offset := 0
	for {
		token := tokenizer.Next()
		if token == html.ErrorToken {
			// An unfinished tag at the end, such as "<a", ends the stream
			// without a token of its own: it is the page's text all the same.
			if offset < len(src) {
				visit(html.TextToken, src[offset:], offset, "")
			}
			return
		}
		// Raw is only valid until the next call, and TagName lower-cases it
		// in place.
		raw := bytes.Clone(tokenizer.Raw())
		name := ""
		if token == html.StartTagToken || token == html.SelfClosingTagToken || token == html.EndTagToken {
			tag, _ := tokenizer.TagName()
			name = string(tag)
		}
		visit(token, raw, offset, name)
		offset += len(raw)
	}
}

// impliedEnds lists, for a start tag, the open elements it ends when one of
// them is the innermost: the end tags HTML lets authors leave out, as a
// second <li> ends the first.
var impliedEnds = func() map[string]map[string]bool {
	ends := map[string]map[string]bool{
		"li": {"li": true}, "dt": {"dt": true, "dd": true}, "dd": {"dt": true, "dd": true},
		"tr": {"tr": true, "td": true, "th": true}, "td": {"td": true, "th": true}, "th": {"td": true, "th": true},
		"thead": {"tbody": true, "tfoot": true}, "tbody": {"thead": true, "tbody": true, "tfoot": true}, "tfoot": {"thead": true, "tbody": true},
		"option": {"option": true}, "optgroup": {"option": true, "optgroup": true},
	}
	// A paragraph ends where a block begins.
	for block := range strings.FieldsSeq("address article aside blockquote details div dl fieldset figcaption figure footer form " +
		"h1 h2 h3 h4 h5 h6 header hgroup hr main menu nav ol p pre section table ul") {
		if ends[block] == nil {
			ends[block] = map[string]bool{}
		}
		ends[block]["p"] = true
	}
	return ends
}()

// Spans returns where each element written in src ends, by the offset of its
// start tag. An end tag closes the nearest open element of its name and any
// opened after it, a start tag closes what impliedEnds says, and elements
// never closed end with src.
func Spans(src []byte) map[int]int {
	spans := map[int]int{}
	type open struct {
		name string
		at   int
	}
	var stack []open
	walk(src, func(token html.TokenType, raw []byte, offset int, name string) {
		if token == html.StartTagToken || token == html.SelfClosingTagToken {
			for len(stack) > 0 && impliedEnds[name][stack[len(stack)-1].name] {
				spans[stack[len(stack)-1].at] = offset
				stack = stack[:len(stack)-1]
			}
		}
		switch token {
		case html.StartTagToken:
			if voidElements[name] {
				spans[offset] = offset + len(raw)
				return
			}
			stack = append(stack, open{name, offset})
		case html.SelfClosingTagToken:
			spans[offset] = offset + len(raw)
		case html.EndTagToken:
			for i, element := range slices.Backward(stack) {
				if element.name != name {
					continue
				}
				for j := len(stack) - 1; j > i; j-- {
					spans[stack[j].at] = offset
				}
				spans[element.at] = offset + len(raw)
				stack = stack[:i]
				return
			}
		}
	})
	for _, o := range stack {
		spans[o.at] = len(src)
	}
	return spans
}

// ElementSource is where an element is written in a version's source.
type ElementSource struct {
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Text      string `json:"text"`
}

// Element returns the source of the element whose start tag is at offset in
// src, or false when no element starts there.
func Element(src []byte, offset int) (ElementSource, bool) {
	end, ok := Spans(src)[offset]
	if !ok {
		return ElementSource{}, false
	}
	return ElementSource{StartLine: LineAt(src, offset), EndLine: LineAt(src, max(offset, end-1)), Text: string(src[offset:end])}, true
}

// LineAt is the 1-based line of offset in src.
func LineAt(src []byte, offset int) int {
	offset = min(max(offset, 0), len(src))
	return bytes.Count(src[:offset], []byte{'\n'}) + 1
}

// Deepest returns the start offsets of the innermost elements that hold
// each position, once each, in the order of the positions.
func Deepest(src []byte, positions []int) []int {
	spans := Spans(src)
	seen := map[int]bool{}
	var result []int
	for _, position := range positions {
		best := -1
		for start, end := range spans {
			if start <= position && position < end && start > best {
				best = start
			}
		}
		if best >= 0 && !seen[best] {
			seen[best] = true
			result = append(result, best)
		}
	}
	return result
}

// CSSRule finds the rule written for selector in src, as a stylesheet lists
// it: the line and the rule's text up to its closing brace.
func CSSRule(src []byte, selector string) (int, string, bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return 0, "", false
	}
	needle := []byte(selector)
	for from := 0; from < len(src); {
		i := bytes.Index(src[from:], needle)
		if i < 0 {
			return 0, "", false
		}
		at := from + i
		rest := bytes.TrimLeft(src[at+len(needle):], " \t\r\n")
		before := bytes.TrimRight(src[:at], " \t")
		if bytes.HasPrefix(rest, []byte("{")) && (len(before) == 0 || bytes.ContainsAny(before[len(before)-1:], "}\n;,{>")) {
			end := bytes.IndexByte(src[at:], '}')
			if end < 0 {
				return 0, "", false
			}
			return LineAt(src, at), string(src[at : at+end+1]), true
		}
		from = at + len(needle)
	}
	return 0, "", false
}

// Title is the text of src's first <title>, with runs of white space folded.
func Title(src []byte) string {
	tokenizer := html.NewTokenizer(bytes.NewReader(src))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			if name, _ := tokenizer.TagName(); string(name) == "title" {
				if tokenizer.Next() != html.TextToken {
					return ""
				}
				return strings.Join(strings.Fields(string(tokenizer.Text())), " ")
			}
		}
	}
}

// foldedData matches an inline data URL whose payload runs on one line for
// 1000 characters or more; Segment shows it as a placeholder. (RE2 counts to
// 1000 at most.)
var foldedData = regexp.MustCompile(`data:([a-zA-Z0-9.+/-]*);base64,[A-Za-z0-9+/=]{1000}[A-Za-z0-9+/=]*`)

// FoldedMarker starts the placeholder of a folded data URL; text an agent
// copies with it cannot be found in the source.
const FoldedMarker = ";base64,…("

// Segment is a part of a version's source for an agent to read.
type Segment struct {
	Text       string
	FirstLine  int
	LastLine   int
	TotalLines int
	// NextLine is where the next segment starts; 0 after the last one.
	NextLine int
	// Truncated says the first line alone was longer than the segment.
	Truncated bool
	Folded    int
}

// ReadSegment returns whole lines of src from line from (1-based) while they
// fit in maxChars characters, with long inline data folded.
func ReadSegment(src []byte, from, maxChars int) Segment {
	lines := strings.SplitAfter(string(src), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	segment := Segment{TotalLines: len(lines)}
	if from < 1 {
		from = 1
	}
	if from > len(lines) {
		return segment
	}
	var text strings.Builder
	count := 0
	folded := 0
	line := from
	for ; line <= len(lines); line++ {
		shown := foldedData.ReplaceAllStringFunc(lines[line-1], func(match string) string {
			folded++
			mime, _, _ := strings.Cut(strings.TrimPrefix(match, "data:"), ";")
			payload := len(match) - strings.Index(match, ",") - 1
			return fmt.Sprintf("data:%s;base64,…(%d KB, #%d)", mime, payload*3/4/1024, folded)
		})
		length := utf8.RuneCountInString(shown)
		if count > 0 && count+length > maxChars {
			break
		}
		if count == 0 && length > maxChars {
			runes := []rune(shown)
			text.WriteString(string(runes[:maxChars]))
			segment.Truncated = true
			line++
			break
		}
		text.WriteString(shown)
		count += length
	}
	segment.Text = text.String()
	segment.FirstLine = from
	segment.LastLine = line - 1
	if line <= len(lines) {
		segment.NextLine = line
	}
	segment.Folded = folded
	return segment
}
