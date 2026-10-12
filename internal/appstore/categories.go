package appstore

// A-NAS categories. The vendored CasaOS categories are too coarse to browse
// by (most apps are "Productivity"), so each app is assigned one of these
// when it is reviewed into the catalog.
const (
	CategoryMedia     = "media"
	CategoryReading   = "reading"
	CategoryPhotos    = "photos"
	CategoryDownloads = "downloads"
	CategoryFiles     = "files"
	CategoryNotes     = "notes"
	CategoryWorkflow  = "workflow"
	CategoryTools     = "tools"
	CategoryNetwork   = "network"
	CategorySystem    = "system"
)

// Categories lists every category in the order the App Center shows them.
var Categories = []string{
	CategoryMedia, CategoryReading, CategoryPhotos, CategoryDownloads,
	CategoryFiles, CategoryNotes, CategoryWorkflow, CategoryTools,
	CategoryNetwork, CategorySystem,
}

// appCategories assigns every vendored app its category;
// TestEveryCatalogAppHasACategory keeps it in step with the catalog.
var appCategories = map[string]string{
	"audiobookshelf": CategoryMedia,
	"navidrome":      CategoryMedia,
	"swingmusic":     CategoryMedia,
	"calibre-web":    CategoryReading,
	"freshrss":       CategoryReading,
	"kavita":         CategoryReading,
	"komga":          CategoryReading,
	"immich":         CategoryPhotos,
	"photoprism":     CategoryPhotos,
	"deluge":         CategoryDownloads,
	"pyload":         CategoryDownloads,
	"qbittorrent":    CategoryDownloads,
	"transmission":   CategoryDownloads,
	"duplicati":      CategoryFiles,
	"filedrop":       CategoryFiles,
	"nextcloud":      CategoryFiles,
	"openlist":       CategoryFiles,
	"psitransfer":    CategoryFiles,
	"resilio-sync":   CategoryFiles,
	"syncthing":      CategoryFiles,
	"linkwarden":     CategoryNotes,
	"memos":          CategoryNotes,
	"siyuan-note":    CategoryNotes,
	"trilium":        CategoryNotes,
	"etherpad":       CategoryWorkflow,
	"excalidraw":     CategoryWorkflow,
	"n8n":            CategoryWorkflow,
	"vocechat":       CategoryWorkflow,
	"actualbudget":   CategoryTools,
	"bentopdf":       CategoryTools,
	"convertx":       CategoryTools,
	"ddns-go":        CategoryNetwork,
	"myspeed":        CategoryNetwork,
	"openspeedtest":  CategoryNetwork,
	"grafana":        CategorySystem,
	"uptimekuma":     CategorySystem,
	"vaultwarden":    CategorySystem,
}
