package httpapi

import "testing"

func TestBrowserPreviewableContent(t *testing.T) {
	for _, name := range []string{"photo.jpg", "scan.PNG", "manual.pdf"} {
		if !browserPreviewableContent(name) {
			t.Fatalf("browserPreviewableContent(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"report.docx", "slides.pptx", "archive.zip", "manual.pdf.exe"} {
		if browserPreviewableContent(name) {
			t.Fatalf("browserPreviewableContent(%q) = true, want false", name)
		}
	}
}
