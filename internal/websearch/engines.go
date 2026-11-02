package websearch

import (
	"cmp"
	"slices"
)

// Category is a tab of the web search window.
type Category string

const (
	Web      Category = "web"
	Images   Category = "images"
	Videos   Category = "videos"
	News     Category = "news"
	Music    Category = "music"
	Map      Category = "map"
	Science  Category = "science"
	IT       Category = "it"
	Social   Category = "social"
	Torrents Category = "torrents"
)

// Categories are the tabs in the order the window shows them.
var Categories = []Category{Web, Images, Videos, News, Music, Map, Science, IT, Social, Torrents}

// tabOf places SearXNG's categories in the window's tabs. An engine joins
// every tab one of its categories names; one that names none, such as a
// recipe site, searches with the web tab. SearXNG's other categories (web,
// wikimedia, blogs, books) only describe an engine.
var tabOf = map[string]Category{
	"general": Web, "dictionaries": Web, "define": Web, "translate": Web, "currency": Web, "weather": Web, "shopping": Web,
	"images": Images, "stock images": Images, "icons": Images,
	"videos": Videos, "movies": Videos,
	"news":  News,
	"music": Music, "lyrics": Music, "radio": Music,
	"map":     Map,
	"science": Science, "scientific publications": Science,
	"it": IT, "packages": IT, "repos": IT, "q&a": IT, "software wikis": IT, "apps": IT, "cargo": IT, "code": IT,
	"social media": Social,
	"files":        Torrents,
}

// EngineInfo is an engine SearXNG loaded, as its /config lists it.
type EngineInfo struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
}

// labels are the names the window shows for engines it has a name for in
// Chinese or with their own capitals; any other engine shows SearXNG's name.
var labels = map[string]string{
	"baidu": "百度", "sogou": "搜狗", "360search": "360 搜索", "quark": "夸克", "bing": "必应", "google": "Google",
	"yandex": "Yandex", "duckduckgo": "DuckDuckGo", "brave": "Brave", "startpage": "Startpage", "qwant": "Qwant",
	"mojeek": "Mojeek", "wikipedia": "维基百科", "wikidata": "维基数据",
	"baidu images": "百度图片", "sogou images": "搜狗图片", "quark images": "夸克图片", "bing images": "必应图片",
	"google images": "Google 图片", "yandex images": "Yandex 图片", "duckduckgo images": "DuckDuckGo 图片",
	"brave.images": "Brave 图片",
	"bilibili":     "哔哩哔哩", "iqiyi": "爱奇艺", "acfun": "AcFun", "sogou videos": "搜狗视频", "360search videos": "360 视频",
	"bing videos": "必应视频", "google videos": "Google 视频", "youtube": "YouTube", "dailymotion": "Dailymotion",
	"duckduckgo videos": "DuckDuckGo 视频", "brave.videos": "Brave 视频", "vimeo": "Vimeo",
	"sogou wechat": "搜狗微信", "chinaso news": "中国搜索资讯", "sina": "新浪", "bing news": "必应资讯",
	"google news": "Google 资讯", "duckduckgo news": "DuckDuckGo 资讯", "brave.news": "Brave 资讯", "yahoo news": "Yahoo 资讯",
	"baidu kaifa": "百度开发者", "piratebay": "海盗湾", "openstreetmap": "OpenStreetMap", "github": "GitHub",
	"stackoverflow": "Stack Overflow",
}

// first are the engines that take their turn ahead of the others in a tab,
// in this order: the Chinese ones, then the large general engines. The rest
// follow by name.
var first = []string{
	"baidu", "sogou", "360search", "quark", "bing", "google", "yandex", "duckduckgo", "brave", "startpage", "qwant",
	"mojeek", "wikipedia", "wikidata",
	"baidu images", "sogou images", "quark images", "bing images", "google images", "yandex images",
	"duckduckgo images", "brave.images",
	"bilibili", "iqiyi", "acfun", "sogou videos", "360search videos", "bing videos", "google videos", "youtube",
	"dailymotion", "duckduckgo videos", "brave.videos", "vimeo",
	"sogou wechat", "chinaso news", "sina", "bing news", "google news", "duckduckgo news", "brave.news", "yahoo news",
	"baidu kaifa", "piratebay",
}

// platforms are video sites whose results play on the site itself, so a
// result names the site rather than its address.
var platforms = map[string]bool{
	"bilibili": true, "iqiyi": true, "acfun": true, "youtube": true, "dailymotion": true, "vimeo": true,
	"niconico": true, "odysee": true, "rumble": true, "bitchute": true,
}

// label is the name the window shows for an engine.
func label(name string) string {
	if l, ok := labels[name]; ok {
		return l
	}
	return name
}

// catalog is each tab's engines, in the order they take turns.
type catalog map[Category][]string

func newCatalog(infos []EngineInfo) catalog {
	tabs := catalog{}
	for _, info := range infos {
		joined := false
		for _, category := range info.Categories {
			if tab, ok := tabOf[category]; ok && !slices.Contains(tabs[tab], info.Name) {
				tabs[tab] = append(tabs[tab], info.Name)
				joined = true
			}
		}
		if !joined {
			tabs[Web] = append(tabs[Web], info.Name)
		}
	}
	for _, names := range tabs {
		slices.SortStableFunc(names, func(a, b string) int {
			ra, rb := rank(a), rank(b)
			if ra != rb {
				return cmp.Compare(ra, rb)
			}
			return cmp.Compare(a, b)
		})
	}
	return tabs
}

func rank(name string) int {
	if i := slices.Index(first, name); i >= 0 {
		return i
	}
	return len(first)
}

// labels are the names the window shows for a tab's engines.
func (c catalog) labels(category Category) []string {
	names := make([]string, 0, len(c[category]))
	for _, name := range c[category] {
		if l := label(name); !slices.Contains(names, l) {
			names = append(names, l)
		}
	}
	return names
}
