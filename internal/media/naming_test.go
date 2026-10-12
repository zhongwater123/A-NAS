package media

import "testing"

func TestParseNameReadsReleaseChineseAndCameraNames(t *testing.T) {
	cases := []struct {
		name                  string
		mode                  nameMode
		title, episodeTitle   string
		year, season, episode int
		marked, camera        bool
	}{
		{name: "The.Wandering.Earth.II.2023.2160p.WEB-DL.H265.DDP5.1", mode: nameMovie, title: "The Wandering Earth II", year: 2023},
		{name: "流浪地球2.2023.2160p.WEB-DL.H265", mode: nameMovie, title: "流浪地球2", year: 2023},
		{name: "Inception (2010)", mode: nameMovie, title: "Inception", year: 2010},
		{name: "1917", mode: nameMovie, title: "1917"},
		{name: "Movie.Name.2019.1080p.x264-GROUP", mode: nameMovie, title: "Movie Name", year: 2019},
		{name: "Breaking.Bad.S01E02.720p.BluRay.x264", mode: nameAuto, title: "Breaking Bad", season: 1, episode: 2, marked: true},
		{name: "Friends - S02E05 - The One With Five Steaks", mode: nameAuto, title: "Friends", episodeTitle: "The One With Five Steaks", season: 2, episode: 5, marked: true},
		{name: "Show.Name.1x03", mode: nameAuto, title: "Show Name", season: 1, episode: 3, marked: true},
		{name: "[字幕组] 繁花 第05集 1080p", mode: nameAuto, title: "繁花", episode: 5, marked: true},
		{name: "漫长的季节.EP03.2023.1080p", mode: nameAuto, title: "漫长的季节", episode: 3, marked: true},
		{name: "庆余年 第二季 第十二集", mode: nameAuto, title: "庆余年", season: 2, episode: 12, marked: true},
		{name: "01", mode: nameShow, episode: 1, marked: true},
		{name: "[05]", mode: nameShow, episode: 5, marked: true},
		{name: "Rocky 2 Extended", mode: nameAuto, title: "Rocky 2"},
		{name: "VID_20240501_123456", mode: nameAuto, camera: true},
		{name: "家庭聚会 2024-05-01", mode: nameAuto, title: "家庭聚会", year: 2024, camera: true},
		// A movies library ignores what looks like an episode marker.
		{name: "Ep 7 Return", mode: nameMovie, title: "Ep 7 Return"},
	}
	for _, test := range cases {
		got := parseName(test.name, test.mode)
		if test.title != "" || test.marked {
			if got.Title != test.title {
				t.Errorf("parseName(%q).Title = %q, want %q", test.name, got.Title, test.title)
			}
		}
		if got.EpisodeTitle != test.episodeTitle || got.Year != test.year && test.title != "" || got.Season != test.season ||
			got.Episode != test.episode || got.Marked != test.marked || got.Camera != test.camera {
			t.Errorf("parseName(%q) = %+v, want season %d episode %d year %d marked %v camera %v episode title %q",
				test.name, got, test.season, test.episode, test.year, test.marked, test.camera, test.episodeTitle)
		}
	}
}

func TestFolderNamesGiveTitlesAndSeasons(t *testing.T) {
	for name, want := range map[string]struct {
		title string
		year  int
	}{
		"流浪地球2 (2023)":        {"流浪地球2", 2023},
		"Breaking Bad (2008)": {"Breaking Bad", 2008},
		"Breaking Bad":        {"Breaking Bad", 0},
		"[4K] 沙丘 2021":        {"沙丘", 2021},
	} {
		if title, year := folderTitle(name); title != want.title || year != want.year {
			t.Errorf("folderTitle(%q) = %q, %d; want %q, %d", name, title, year, want.title, want.year)
		}
	}
	for name, want := range map[string]int{"Season 1": 1, "season.02": 2, "S03": 3, "第一季": 1, "第十二季": 12, "Specials": 0} {
		if season, ok := seasonOfFolder(name); !ok || season != want {
			t.Errorf("seasonOfFolder(%q) = %d, %v; want %d", name, season, ok, want)
		}
	}
	for _, name := range []string{"漫长的季节", "Season Finale Party", "庆余年 第二季"} {
		if _, ok := seasonOfFolder(name); ok {
			t.Errorf("seasonOfFolder(%q) recognised a season folder", name)
		}
	}
}

func TestChineseNumbersAndSubtitleLanguages(t *testing.T) {
	for value, want := range map[string]int{"十": 10, "十二": 12, "二十": 20, "二十三": 23, "一百零五": 105, "07": 7, "两": 2} {
		if got := chineseNumber(value); got != want {
			t.Errorf("chineseNumber(%q) = %d, want %d", value, got, want)
		}
	}
	for marker, want := range map[string]string{".chs": "zh-Hans", ".zh-CN": "zh-Hans", ".cht": "zh-Hant", ".en.forced": "en", " [eng]": "en", "": ""} {
		if got, _ := subtitleLanguage(marker); got != want {
			t.Errorf("subtitleLanguage(%q) = %q, want %q", marker, got, want)
		}
	}
}

func TestNFOFilesProvideTitlesPlotsAndSets(t *testing.T) {
	movie, err := parseNFO([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>
<movie>
  <title>流浪地球2</title>
  <originaltitle>The Wandering Earth II</originaltitle>
  <ratings><rating name="imdb"><value>6.9</value></rating><rating name="douban" default="true"><value>8.3</value></rating></ratings>
  <plot>太阳即将毁灭。</plot>
  <genre>科幻 / 冒险</genre>
  <genre>灾难</genre>
  <premiered>2023-01-22</premiered>
  <set><name>流浪地球系列</name></set>
</movie>
https://www.themoviedb.org/movie/842675`))
	if err != nil {
		t.Fatalf("parseNFO(movie) error = %v", err)
	}
	if movie.Root != "movie" || movie.Title != "流浪地球2" || movie.year() != 2023 || movie.rating() != 8.3 || movie.setName() != "流浪地球系列" {
		t.Fatalf("movie NFO = %+v, year %d, rating %v, set %q", movie, movie.year(), movie.rating(), movie.setName())
	}
	if genres := movie.genres(); len(genres) != 3 || genres[0] != "科幻" || genres[2] != "灾难" {
		t.Fatalf("genres = %v", genres)
	}
	// GBK-encoded NFO files declare their encoding.
	gbk := append([]byte(`<?xml version="1.0" encoding="GBK"?><tvshow><title>`), 0xb7, 0xb1, 0xbb, 0xa8)
	gbk = append(gbk, []byte(`</title><year>2023</year><set>Plain set</set></tvshow>`)...)
	show, err := parseNFO(gbk)
	if err != nil {
		t.Fatalf("parseNFO(GBK) error = %v", err)
	}
	if show.Root != "tvshow" || show.Title != "繁花" || show.year() != 2023 || show.setName() != "Plain set" {
		t.Fatalf("show NFO = %+v", show)
	}
	if _, err := parseNFO([]byte("https://www.themoviedb.org/movie/842675\n")); err == nil {
		t.Fatal("a URL-only NFO parsed as a document")
	}
}
