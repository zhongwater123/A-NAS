package websearch

import "strings"

// SearXNG's pinned /config exposes names, not remote hosts. These groups
// cover its multi-engine websites; unknown engines retain their own budget.
// Review alongside deploy/search/searxng.json when updating SearXNG.
var websiteGroups = map[string]string{
	"360search":    "360search videos",
	"baidu":        "baidu images|baidu kaifa",
	"bing":         "bing images|bing news|bing videos",
	"brave":        "brave.images|brave.videos|brave.news",
	"dogpile":      "dogpile images|dogpile videos|dogpile news",
	"duckduckgo":   "duckduckgo web|duckduckgo images|duckduckgo videos|duckduckgo news|duckduckgo weather|ddg definitions",
	"findborg":     "findborg images|findborg videos|findborg news",
	"findfiles":    "findfiles images|findfiles videos|findfiles music",
	"fireball":     "fireball news|fireball videos",
	"google":       "google images|google news|google videos|google cse|google cse images|google scholar|google play apps|google play movies",
	"huggingface":  "huggingface datasets|huggingface spaces",
	"lemmy":        "lemmy communities|lemmy users|lemmy posts|lemmy comments",
	"luxxle":       "luxxle images|luxxle videos|luxxle news",
	"mastodon":     "mastodon users|mastodon hashtags",
	"mojeek":       "mojeek images|mojeek news",
	"naver":        "naver images|naver news|naver videos",
	"openaire":     "openairedatasets|openairepublications",
	"piped":        "piped.music",
	"pixabay":      "pixabay images|pixabay videos",
	"privacywall":  "privacywall images|privacywall videos",
	"quark":        "quark images",
	"qwant":        "qwant news|qwant images|qwant videos",
	"resulthunter": "resulthunter images",
	"searchrockit": "searchrockit images|searchrockit news",
	"searchzee":    "searchzee news",
	"sogou":        "sogou images|sogou videos|sogou wechat",
	"startpage":    "startpage news|startpage images",
	"startpagina":  "startpagina images|startpagina videos|startpagina news",
	"swisscows":    "swisscows images|swisscows videos|swisscows news",
	"tiger":        "tiger news",
	"tonline":      "tonline images|tonline videos|tonline news",
	"tusksearch":   "tusksearch images|tusksearch videos|tusksearch news",
	"vuhuv":        "vuhuv images|vuhuv videos",
	"wikicommons":  "wikicommons.images|wikicommons.videos|wikicommons.audio|wikicommons.files",
	"xprivo":       "xprivo news|xprivo videos",
	"yacy":         "yacy images",
	"yahoo":        "yahoo news",
	"yandex":       "yandex images|yandex music",
}

var engineWebsites = func() map[string]string {
	sites := map[string]string{}
	for site, engines := range websiteGroups {
		for engine := range strings.SplitSeq(engines, "|") {
			sites[engine] = site
		}
	}
	return sites
}()

func website(engine string) string {
	if site, ok := engineWebsites[engine]; ok {
		return site
	}
	return engine
}
