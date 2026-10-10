package media

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	ptn "github.com/razsteinmetz/go-ptn"
)

// Release names ("Title.2019.1080p.x264-GROUP") are split by go-ptn. The
// rules here only add what it does not know: the Plex/Jellyfin/Kodi folder
// conventions, Chinese season and episode markers, and camera file names.

var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".mov": true, ".avi": true, ".wmv": true,
	".flv": true, ".ts": true, ".m2ts": true, ".mts": true, ".mpg": true, ".mpeg": true, ".rmvb": true,
	".rm": true, ".3gp": true, ".vob": true, ".ogv": true, ".asf": true, ".divx": true,
}

var imageExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

var subtitleExtensions = map[string]string{".srt": "srt", ".ass": "ass", ".ssa": "ass", ".vtt": "webvtt"}

func isVideo(name string) bool { return videoExtensions[strings.ToLower(path.Ext(name))] }

func baseName(name string) string { return strings.TrimSuffix(name, path.Ext(name)) }

type parsedName struct {
	Title        string
	Year         int
	Season       int
	HasSeason    bool
	Episode      int
	EpisodeTitle string
	// Marked is true when the name carries an explicit episode marker.
	Marked bool
	// Camera names and dates point to home videos rather than films.
	Camera bool
}

var (
	seasonEpisode  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[ ._-]*e(\d{1,3})(?:[^0-9]|$)`)
	crossEpisode   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)
	chineseEpisode = regexp.MustCompile(`第\s*([0-9]{1,4}|[零〇一二两三四五六七八九十百]+)\s*[集话話期]`)
	chineseSeason  = regexp.MustCompile(`第\s*([0-9]{1,2}|[零〇一二两三四五六七八九十]+)\s*[季部]`)
	episodeMarker  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ep?[ ._-]?(\d{1,3})(?:v\d)?(?:[^0-9a-z]|$)`)
	bracketNumber  = regexp.MustCompile(`[\[【](\d{1,3})(?:v\d)?[\]】]`)
	looseNumber    = regexp.MustCompile(`(?:^|[ ._\-])(\d{1,3})(?:v\d)?(?:[ ._\-]|$)`)
	leadingGroups  = regexp.MustCompile(`^(?:\s*[\[【][^\]】]*[\]】]\s*)+`)
	cameraName     = regexp.MustCompile(`(?i)^(?:vid|img|mvi|dsc|pxl|mov|dji|gopr|gh|wechat|mmexport|lv)[ _-]?\d|^(?:screen ?recording|录屏|微信视频)|\d{8}[ _-]?\d{6}|(?:19|20)\d\d[-_.](?:0[1-9]|1[0-2])[-_.](?:0[1-9]|[12]\d|3[01])`)
	seasonFolder   = regexp.MustCompile(`(?i)^(?:season|series|s)[ ._-]*(\d{1,2})$`)
	yearInFolder   = regexp.MustCompile(`[(\[（]?((?:19|20)\d\d)[)\]）]?\s*$`)
	genericTitle   = regexp.MustCompile(`(?i)^(?:movie|video|film|main|feature|sample|trailer|cd\s?\d|dis[ck]\s?\d|part\s?\d|e?p?\d{1,3}|s\d{1,2}e\d{1,3})$`)
	resolutionLike = map[int]bool{144: true, 240: true, 360: true, 480: true, 540: true, 576: true, 720: true}
)

type nameMode int

const (
	// nameMovie ignores episode markers: everything in a movies library is a film.
	nameMovie nameMode = iota
	// nameAuto recognises explicit episode markers only.
	nameAuto
	// nameShow also takes bare numbers ("01.mp4", "[05]") as episode numbers.
	nameShow
)

// parseName reads a video file name without its extension.
func parseName(base string, mode nameMode) parsedName {
	var result parsedName
	name := strings.TrimSpace(base)
	result.Camera = cameraName.MatchString(name)
	stripped := strings.TrimSpace(leadingGroups.ReplaceAllString(name, ""))
	if stripped == "" {
		stripped = name
	}
	cut := -1
	if mode == nameMovie {
		// Fall through to the title with no marker.
	} else if match := seasonEpisode.FindStringSubmatchIndex(stripped); match != nil {
		result.Season, _ = strconv.Atoi(stripped[match[2]:match[3]])
		result.Episode, _ = strconv.Atoi(stripped[match[4]:match[5]])
		result.Marked, result.HasSeason, cut = true, true, match[0]
		result.EpisodeTitle = episodeTitleAfter(stripped[match[5]:])
	} else if match := crossEpisode.FindStringSubmatchIndex(stripped); match != nil {
		result.Season, _ = strconv.Atoi(stripped[match[2]:match[3]])
		result.Episode, _ = strconv.Atoi(stripped[match[4]:match[5]])
		result.Marked, result.HasSeason, cut = true, true, match[0]
		result.EpisodeTitle = episodeTitleAfter(stripped[match[5]:])
	} else if match := chineseEpisode.FindStringSubmatchIndex(stripped); match != nil {
		result.Episode = chineseNumber(stripped[match[2]:match[3]])
		result.Marked, cut = result.Episode > 0, match[0]
	} else if match := episodeMarker.FindStringSubmatchIndex(stripped); match != nil {
		result.Episode, _ = strconv.Atoi(stripped[match[2]:match[3]])
		result.Marked, cut = result.Episode > 0, match[0]
	} else if mode == nameShow {
		if match := bracketNumber.FindStringSubmatchIndex(stripped); match != nil {
			result.Episode, _ = strconv.Atoi(stripped[match[2]:match[3]])
			result.Marked, cut = result.Episode > 0, match[0]
		} else if match := looseNumber.FindAllStringSubmatchIndex(stripped, -1); match != nil {
			for _, candidate := range match {
				number, _ := strconv.Atoi(stripped[candidate[2]:candidate[3]])
				if number > 0 && !resolutionLike[number] {
					result.Episode, result.Marked, cut = number, true, candidate[0]
					break
				}
			}
		}
	}
	if match := chineseSeason.FindStringSubmatch(stripped); match != nil && !result.HasSeason && mode != nameMovie {
		result.Season, result.HasSeason = chineseNumber(match[1]), true
	}
	titlePart := stripped
	if cut > 0 {
		titlePart = stripped[:cut]
	} else if cut == 0 {
		titlePart = ""
	}
	if mode != nameMovie {
		titlePart = chineseSeason.ReplaceAllString(titlePart, "")
	}
	if titlePart = strings.TrimSpace(titlePart); titlePart != "" {
		info, _ := ptn.Parse(titlePart)
		result.Title = cleanTitle(info.Title)
		result.Year = info.Year
		if result.Title == "" && info.Year != 0 {
			result.Title = strconv.Itoa(info.Year)
			result.Year = 0
		}
	}
	if genericTitle.MatchString(result.Title) {
		result.Title = ""
	}
	return result
}

func episodeTitleAfter(rest string) string {
	rest = strings.TrimLeft(rest, " ._-")
	if rest == "" {
		return ""
	}
	info, _ := ptn.Parse(rest)
	title := cleanTitle(info.Title)
	if genericTitle.MatchString(title) {
		return ""
	}
	return title
}

func cleanTitle(title string) string {
	title = strings.TrimSpace(strings.Trim(title, " -_.[]【】()（）"))
	return strings.Join(strings.Fields(title), " ")
}

// folderTitle reads "Title (Year)" folder names.
func folderTitle(name string) (string, int) {
	name = strings.TrimSpace(leadingGroups.ReplaceAllString(name, ""))
	if match := yearInFolder.FindStringSubmatchIndex(name); match != nil {
		year, _ := strconv.Atoi(name[match[2]:match[3]])
		if title := cleanTitle(name[:match[0]]); title != "" {
			return title, year
		}
	}
	info, _ := ptn.Parse(name)
	if title := cleanTitle(info.Title); title != "" {
		return title, info.Year
	}
	return cleanTitle(name), 0
}

// seasonOfFolder recognises "Season 1", "S01", "第一季" and "Specials".
func seasonOfFolder(name string) (int, bool) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "specials") || name == "特别篇" || name == "番外" {
		return 0, true
	}
	if match := seasonFolder.FindStringSubmatch(name); match != nil {
		season, _ := strconv.Atoi(match[1])
		return season, true
	}
	if match := chineseSeason.FindStringSubmatch(name); match != nil && strings.TrimSpace(chineseSeason.ReplaceAllString(name, "")) == "" {
		return chineseNumber(match[1]), true
	}
	return 0, false
}

var chineseDigits = map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

// chineseNumber reads decimal digits or Chinese numerals up to 999.
func chineseNumber(value string) int {
	if number, err := strconv.Atoi(value); err == nil {
		return number
	}
	total, current := 0, 0
	for _, r := range value {
		switch r {
		case '十':
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
		case '百':
			if current == 0 {
				current = 1
			}
			total += current * 100
			current = 0
		default:
			digit, ok := chineseDigits[r]
			if !ok {
				return 0
			}
			current = current*10 + digit
		}
	}
	return total + current
}

// normalizeTitle keys shows: case, spacing and punctuation do not matter.
func normalizeTitle(title string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// subtitleLanguage reads the language marker between a video's base name and
// a subtitle's extension, as in "Movie.chs.srt" or "Movie.en.forced.srt".
func subtitleLanguage(marker string) (language, label string) {
	tokens := strings.FieldsFunc(strings.ToLower(marker), func(r rune) bool { return strings.ContainsRune("._- []()", r) })
	for _, token := range tokens {
		switch token {
		case "chs", "sc", "zh", "zho", "chi", "cn", "hans", "gb", "简体", "简中", "简":
			return "zh-Hans", "简体中文"
		case "cht", "tc", "hant", "big5", "tw", "hk", "繁体", "繁中", "繁":
			return "zh-Hant", "繁体中文"
		case "en", "eng", "english", "英文", "英":
			return "en", "English"
		case "ja", "jp", "jpn", "日文", "日":
			return "ja", "日本語"
		case "ko", "kor", "韩文":
			return "ko", "한국어"
		case "chs&eng", "双语", "中英":
			return "zh-Hans", "中英双语"
		}
	}
	return "", ""
}
