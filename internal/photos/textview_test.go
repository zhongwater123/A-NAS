package photos_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// An ID card with made-up numbers whose check digits hold, a phone number
// and a long serial number, none a real person's.
const (
	idCardText = "姓名 张三\n性别 男 民族 汉\n出生 1990年1月1日\n住址 某市某路1号\n公民身份号码 440306199001011236\n" +
		"电话 138 1234 5678\n编号 123456789012345"
	idCardAnswer = `{"姓名": "张三", "性别": "男", "民族": "汉", "出生": "1990年1月1日", "住址": "某市某路1号", ` +
		`"公民身份号码": "440306199001011236"}`
)

// handles matches the handles an assistant gets for sensitive values.
var handles = regexp.MustCompile(`⟦[^⟧]*⟧`)

// idCardService reads every red photo as idCardText.
func idCardService(t *testing.T) (*photos.Service, *readingEmbedder) {
	t.Helper()
	embedder := newReadingEmbedder()
	embedder.text, embedder.answer = idCardText, idCardAnswer
	return routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1")), embedder
}

func TestCorrectionsBelongToThePhotoInEveryLibrary(t *testing.T) {
	embedder := newReadingEmbedder()
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	libraries(t, service, namedBob)
	receipt := importPhoto(t, service, namedAlice, shared.ID, "", "receipt.png", red)
	index(t, service, embedder)

	text, err := service.Text(ctx, namedBob, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(text.Fields))
	for i, field := range text.Fields {
		names[i] = field.Name
	}
	if text.Status != photos.TextRead || text.Kind != "receipt" || !slices.Equal(names, []string{"商户", "日期", "合计"}) {
		t.Fatalf("Text() = %+v", text)
	}
	if total := fieldNamed(t, text, "合计"); total.Value != "¥35.80" || total.Passed || total.Correction != nil {
		t.Fatalf("合计 as read = %+v", total)
	}

	// Bob corrects it in the shared library; Alice's copy in her own library
	// shows the correction too.
	if _, err := service.CorrectText(ctx, namedBob, receipt.ID, "合计", " ¥35.50 "); err != nil {
		t.Fatal(err)
	}
	copied, err := service.Copy(ctx, namedAlice, receipt.ID, private.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	text, err = service.Text(ctx, namedAlice, copied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if total := fieldNamed(t, text, "合计"); total.Value != "¥35.50" || total.Read != "¥35.80" ||
		total.Correction == nil || total.Correction.Username != "bob" {
		t.Fatalf("合计 in Alice's copy = %+v", total)
	}
	// The same value again changes nothing; Alice takes Bob's correction
	// back from her library, and the field shows what was read.
	if _, err := service.CorrectText(ctx, namedAlice, copied.ID, "合计", "¥35.50"); err != nil {
		t.Fatal(err)
	}
	if text, err = service.RestoreText(ctx, namedAlice, copied.ID, "合计"); err != nil {
		t.Fatal(err)
	}
	if total := fieldNamed(t, text, "合计"); total.Value != "¥35.80" || total.Correction != nil {
		t.Fatalf("合计 restored = %+v", total)
	}
	if _, err := service.RestoreText(ctx, namedAlice, copied.ID, "合计"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("restoring a field not corrected: error = %v", err)
	}

	// Everyone who sees the photo sees who changed what, from which library.
	changes, err := service.TextChanges(ctx, namedBob, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want Bob's correction and Alice's restore", changes)
	}
	restored, corrected := changes[0], changes[1]
	if !restored.Restored || restored.Username != "alice" || restored.Old != "¥35.50" || restored.New != "¥35.80" ||
		restored.Library.ID != private.ID || restored.Library.OwnerName != "alice" {
		t.Fatalf("newest change = %+v", restored)
	}
	if corrected.Restored || corrected.Username != "bob" || corrected.Old != "¥35.80" || corrected.New != "¥35.50" ||
		corrected.Library.ID != shared.ID || corrected.Library.Kind != photos.LibraryKindShared {
		t.Fatalf("first change = %+v", corrected)
	}
}

func TestACorrectionMustFitAFieldOfAReadingSomeoneMayChange(t *testing.T) {
	embedder := newReadingEmbedder()
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	receipt := importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	sea := importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, embedder)
	viewer := photos.Principal{UserID: "user:admin", Username: "admin", Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "viewing:1", OwnerUserID: namedAlice.UserID, ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)},
	}}
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}

	for name, attempt := range map[string]struct {
		p          photos.Principal
		asset      string
		field, val string
		want       error
	}{
		"too long":                  {namedAlice, receipt.ID, "合计", strings.Repeat("9", photos.MaxCorrectionRunes+1), photos.ErrInvalidText},
		"a control character":       {namedAlice, receipt.ID, "合计", "35\x0050", photos.ErrInvalidText},
		"no such field":             {namedAlice, receipt.ID, "卡号", "1", photos.ErrNotFound},
		"a photo not read":          {namedAlice, sea.ID, "合计", "1", photos.ErrConflict},
		"through a viewing grant":   {viewer, receipt.ID, "合计", "1", photos.ErrForbidden},
		"as an assistant":           {assistant, receipt.ID, "合计", "1", photos.ErrForbidden},
		"by someone who cannot see": {namedBob, receipt.ID, "合计", "1", photos.ErrNotFound},
	} {
		if _, err := service.CorrectText(ctx, attempt.p, attempt.asset, attempt.field, attempt.val); !errors.Is(err, attempt.want) {
			t.Errorf("%s: error = %v, want %v", name, err, attempt.want)
		}
	}
	// The viewing administrator reads the text and its changes, read-only.
	if text, err := service.Text(ctx, viewer, receipt.ID); err != nil || text.Status != photos.TextRead {
		t.Fatalf("Text() through a viewing grant = %+v, %v", text, err)
	}
	if _, err := service.TextChanges(ctx, viewer, receipt.ID); err != nil {
		t.Fatalf("TextChanges() through a viewing grant: %v", err)
	}
	// A photo in the trash keeps its text as it is.
	if _, err := service.Trash(ctx, namedAlice, receipt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CorrectText(ctx, namedAlice, receipt.ID, "合计", "1"); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("correcting a trashed photo: error = %v", err)
	}
}

func TestNoOneReadsOrFindsTheTextOfPhotosTheyCannotSee(t *testing.T) {
	service, embedder := idCardService(t)
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	libraries(t, service, namedBob)
	card := importPhoto(t, service, namedAlice, private.ID, "", "card.png", red)
	index(t, service, embedder)

	if _, err := service.Text(ctx, namedBob, card.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Bob's Text() of Alice's photo: error = %v", err)
	}
	if _, err := service.TextChanges(ctx, namedBob, card.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Bob's TextChanges() of Alice's photo: error = %v", err)
	}
	for _, query := range []string{"张三", "4403", "某市某路"} {
		if page := search(t, service, namedBob, query); page.Texts != 0 || slices.ContainsFunc(page.Assets, func(asset photos.Asset) bool {
			return asset.ID == card.ID
		}) {
			t.Errorf("Bob's search for %q found Alice's photo: %+v", query, page)
		}
		// Alice finds it by its text, a number also by a part spaced
		// otherwise.
		if page := search(t, service, namedAlice, query); page.Texts != 1 || page.Assets[0].ID != card.ID {
			t.Errorf("Alice's search for %q = %+v", query, page)
		}
	}
	if page := search(t, service, namedAlice, "4403 0619-9001"); page.Texts != 1 {
		t.Errorf("a number spaced and hyphenated otherwise = %+v", page)
	}
}

func TestTheAssistantGetsHandlesInsteadOfSensitiveValues(t *testing.T) {
	service, embedder := idCardService(t)
	ctx := context.Background()
	_, shared := libraries(t, service, namedAlice)
	card := importPhoto(t, service, namedAlice, shared.ID, "", "card.png", red)
	index(t, service, embedder)
	if _, err := service.CorrectText(ctx, namedAlice, card.ID, "住址", "某市某路2号"); err != nil {
		t.Fatal(err)
	}
	alicesAssistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}
	bobsAssistant := photos.Principal{UserID: namedBob.UserID, Username: "bob", Assistant: true}

	text, err := service.Text(ctx, alicesAssistant, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	// Not one of its sensitive values, nor any part of a number, outside the
	// handles' random letters: the birth date and the name are no secret.
	unhandled := handles.ReplaceAllString(string(encoded), "")
	for _, secret := range []string{"440306", "1990010", "1236", "138", "1234 5678", "12345678", "某市某路1号", "某市某路2号", "某路"} {
		if strings.Contains(unhandled, secret) {
			t.Errorf("the assistant's text holds %q: %s", secret, encoded)
		}
	}
	number := fieldNamed(t, text, "公民身份号码")
	if !strings.HasPrefix(number.Value, "⟦身份证号·") || number.Read != number.Value || !strings.Contains(text.Text, number.Value) {
		t.Fatalf("公民身份号码 for the assistant = %+v in %q", number, text.Text)
	}
	if name := fieldNamed(t, text, "姓名"); name.Value != "张三" {
		t.Fatalf("姓名 for the assistant = %+v", name)
	}
	if !strings.Contains(text.Text, "⟦手机号·") || !strings.Contains(text.Text, "⟦号码·") {
		t.Fatalf("the numbers of the text for the assistant: %q", text.Text)
	}
	// One handle for each value: the address, the ID number, the phone
	// number and the serial number.
	if found := slices.Compact(slices.Sorted(slices.Values(handles.FindAllString(text.Text, -1)))); len(found) != 4 {
		t.Fatalf("handles in the assistant's text = %v", found)
	}
	// A handle stays the same for its member, and another member's
	// assistant gets its own.
	again, err := service.Text(ctx, alicesAssistant, card.ID)
	if err != nil || fieldNamed(t, again, "公民身份号码").Value != number.Value {
		t.Fatalf("the handle changed: %+v, %v", again, err)
	}
	bobs, err := service.Text(ctx, bobsAssistant, card.ID)
	if err != nil || fieldNamed(t, bobs, "公民身份号码").Value == number.Value {
		t.Fatalf("Bob's assistant's handle = %+v, %v", bobs, err)
	}

	// It cannot learn a number by searching for parts of it, but finds what
	// is no secret; it changes nothing and reads no changes.
	for _, query := range []string{"4403", "1236", "1381234", "某路", "12345678901"} {
		if page := search(t, service, alicesAssistant, query); page.Texts != 0 {
			t.Errorf("the assistant's search for %q found the photo by its text", query)
		}
	}
	if page := search(t, service, alicesAssistant, "张三"); page.Texts != 1 {
		t.Errorf("the assistant's search for 张三 = %+v", page)
	}
	if _, err := service.TextChanges(ctx, alicesAssistant, card.ID); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("the assistant's TextChanges(): error = %v", err)
	}
}

func TestTextMatchesFollowNamesAndCountOnEveryPage(t *testing.T) {
	embedder := newReadingEmbedder()
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "超市.png", blue)
	receipt := importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "b.png", green)
	index(t, service, embedder)

	page, err := service.Search(ctx, namedAlice, photos.SearchRequest{Query: "超市", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The name match, the text match, then the one other photo, the
	// closest by similarity.
	if len(page.Assets) != 1 || page.Assets[0].Name != "超市.png" || page.Texts != 1 || page.Closest != 3 || page.Next == "" {
		t.Fatalf("first page = %+v", page)
	}
	page, err = service.Search(ctx, namedAlice, photos.SearchRequest{Query: "超市", Limit: 1, Cursor: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Assets) != 1 || page.Assets[0].ID != receipt.ID || page.Texts != 1 {
		t.Fatalf("second page = %+v", page)
	}

	// A corrected value is found too; with reading off, the library finds
	// nothing by text.
	if _, err := service.CorrectText(ctx, namedAlice, receipt.ID, "商户", "城南便利店"); err != nil {
		t.Fatal(err)
	}
	if page := search(t, service, namedAlice, "城南便利店"); page.Texts != 1 {
		t.Fatalf("search for a corrected value = %+v", page)
	}
	if _, err := service.SetTexts(ctx, namedAlice, private.ID, false); err != nil {
		t.Fatal(err)
	}
	if page := search(t, service, namedAlice, "城南便利店"); page.Texts != 0 {
		t.Fatalf("search with reading off = %+v", page)
	}
}
