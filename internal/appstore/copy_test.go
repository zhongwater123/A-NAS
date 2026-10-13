package appstore

import (
	"strings"
	"testing"
)

// What an app's page says must hold for an A-NAS: no Zima marketing or
// unlimited storage in a description, no CasaOS settings in the notes it
// shows first.
func TestCatalogTextIsWrittenForANAS(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range entries {
		app := entry.App
		listed[app.ID] = true
		description := strings.ToLower(app.Description)
		for _, claim := range []string{"zima", "无限存储", "unlimited storage"} {
			if strings.Contains(description, claim) {
				t.Errorf("%s description still says %q", app.ID, claim)
			}
		}
		if app.Description == "" && app.Tagline == "" {
			t.Errorf("%s has nothing left to describe it", app.ID)
		}
		// "casaos" alone is a default password some manifests set.
		for _, step := range []string{"CasaOS", "Zima", "zima", "Volumes", "右上角", "/DATA/AppData"} {
			if strings.Contains(app.Notes, step) {
				t.Errorf("%s notes still say %q", app.ID, step)
			}
		}
		if _, ok := appNotes[app.ID]; ok && app.UpstreamNotes == "" {
			t.Errorf("%s has A-NAS notes but lost the manifest's own", app.ID)
		}
	}
	for id := range appNotes {
		if !listed[id] {
			t.Errorf("%s has A-NAS notes but is not in the catalog", id)
		}
	}
}

func TestVendorPitchLeavesTheRestOfADescription(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "paragraph",
			in:   "Immich 管理照片。\n\n人脸识别与 4K 播放。\n\n在 Zima 等私有云设备上部署 Immich，享受无限存储容量。",
			want: "Immich 管理照片。\n\n人脸识别与 4K 播放。",
		},
		{
			name: "section under a heading",
			in:   "# 释放潜力\n\n实时同步。\n\n# Syncthing在Zima上的力量\n\n享受无限存储容量。\n\n更多说明。\n\n# 了解更多\n\n- [GitHub](https://github.com)",
			want: "# 释放潜力\n\n实时同步。\n\n# 了解更多\n\n- [GitHub](https://github.com)",
		},
		{
			name: "bold lead-in",
			in:   "**功能丰富**。支持 RSS。\n\n**与 Zima 等私有云的无缝集成**。几乎无限的存储空间。",
			want: "**功能丰富**。支持 RSS。",
		},
		{name: "untouched", in: "#hashtag 不是标题\n\n两段。", want: "#hashtag 不是标题\n\n两段。"},
	}
	for _, c := range cases {
		if got := withoutVendorPitch(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
