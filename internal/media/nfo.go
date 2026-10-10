package media

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// maxNFO bounds the NFO files read during a scan.
const maxNFO = 1 << 20

// nfo is the part of a Kodi NFO file (movie, tvshow or episodedetails) the
// media center shows. Unknown elements are ignored.
type nfo struct {
	Root          string
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	SortTitle     string   `xml:"sorttitle"`
	Year          string   `xml:"year"`
	Premiered     string   `xml:"premiered"`
	Aired         string   `xml:"aired"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Tagline       string   `xml:"tagline"`
	Genres        []string `xml:"genre"`
	Rating        string   `xml:"rating"`
	Ratings       struct {
		Rating []struct {
			Default string `xml:"default,attr"`
			Value   string `xml:"value"`
		} `xml:"rating"`
	} `xml:"ratings"`
	Set struct {
		Name  string `xml:"name"`
		Plain string `xml:",chardata"`
	} `xml:"set"`
	Season  string `xml:"season"`
	Episode string `xml:"episode"`
}

var errNotNFO = errors.New("not a Kodi NFO document")

// parseNFO decodes the first XML element of an NFO file. Kodi also accepts
// files that hold only a scraper URL, which carry nothing to show.
func parseNFO(contents []byte) (nfo, error) {
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	decoder.Strict = false
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		encoding, err := htmlindex.Get(label)
		if err != nil {
			return nil, err
		}
		return encoding.NewDecoder().Reader(input), nil
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nfo{}, errNotNFO
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "movie", "tvshow", "episodedetails", "musicvideo":
		default:
			return nfo{}, errNotNFO
		}
		var document nfo
		if err := decoder.DecodeElement(&document, &start); err != nil {
			return nfo{}, err
		}
		document.Root = start.Name.Local
		return document, nil
	}
}

func (n nfo) year() int {
	for _, value := range []string{n.Year, n.Premiered, n.Aired} {
		value = strings.TrimSpace(value)
		if len(value) >= 4 {
			if year, err := strconv.Atoi(value[:4]); err == nil && year > 1870 && year < 2200 {
				return year
			}
		}
	}
	return 0
}

func (n nfo) plot() string {
	for _, value := range []string{n.Plot, n.Outline, n.Tagline} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (n nfo) rating() float64 {
	values := []string{n.Rating}
	for _, rating := range n.Ratings.Rating {
		if rating.Default == "true" {
			values = append([]string{rating.Value}, values...)
		} else {
			values = append(values, rating.Value)
		}
	}
	for _, value := range values {
		if rating, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && rating > 0 && rating <= 10 {
			return rating
		}
	}
	return 0
}

func (n nfo) genres() []string {
	seen := map[string]bool{}
	var genres []string
	for _, value := range n.Genres {
		for _, genre := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '|' || r == ',' || r == '，' }) {
			if genre = strings.TrimSpace(genre); genre != "" && !seen[genre] {
				seen[genre] = true
				genres = append(genres, genre)
			}
		}
	}
	return genres
}

func (n nfo) setName() string {
	if name := strings.TrimSpace(n.Set.Name); name != "" {
		return name
	}
	return strings.TrimSpace(n.Set.Plain)
}

func nfoNumber(value string) int {
	number, _ := strconv.Atoi(strings.TrimSpace(value))
	return number
}
