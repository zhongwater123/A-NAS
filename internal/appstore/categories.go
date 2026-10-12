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
	CategorySystem    = "system"
)

// Categories lists every category in the order the App Center shows them.
var Categories = []string{
	CategoryMedia, CategoryReading, CategoryPhotos, CategoryDownloads,
	CategoryFiles, CategoryNotes, CategoryWorkflow, CategorySystem,
}

// appCategories assigns every vendored app its category;
// TestEveryCatalogAppHasACategory keeps it in step with the catalog.
var appCategories = map[string]string{
	"audiobookshelf": CategoryMedia,
	"navidrome":      CategoryMedia,
	"calibre-web":    CategoryReading,
	"freshrss":       CategoryReading,
	"kavita":         CategoryReading,
	"komga":          CategoryReading,
	"immich":         CategoryPhotos,
	"photoprism":     CategoryPhotos,
	"qbittorrent":    CategoryDownloads,
	"transmission":   CategoryDownloads,
	"duplicati":      CategoryFiles,
	"filedrop":       CategoryFiles,
	"nextcloud":      CategoryFiles,
	"openlist":       CategoryFiles,
	"psitransfer":    CategoryFiles,
	"syncthing":      CategoryFiles,
	"linkwarden":     CategoryNotes,
	"memos":          CategoryNotes,
	"siyuan-note":    CategoryNotes,
	"trilium":        CategoryNotes,
	"etherpad":       CategoryWorkflow,
	"excalidraw":     CategoryWorkflow,
	"n8n":            CategoryWorkflow,
	"grafana":        CategorySystem,
	"uptimekuma":     CategorySystem,
	"vaultwarden":    CategorySystem,
}
