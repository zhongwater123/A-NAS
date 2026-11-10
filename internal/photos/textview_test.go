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
	// A corrected value needs no one to look at it any more.
	if total := fieldNamed(t, text, "合计"); total.Value != "¥35.50" || total.Read != "¥35.80" ||
		total.Correction == nil || total.Correction.Username != "bob" || !total.Passed || len(total.Reasons) != 0 {
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
	if _, err := service.CorrectText(ctx, namedAlice, card.ID, "公民身份号码", "440306199001011252"); err != nil {
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
	// handles' random letters: the birth date, the name and the address are
	// no secret.
	unhandled := handles.ReplaceAllString(string(encoded), "")
	for _, secret := range []string{"440306", "1990010", "1236", "1252", "138", "1234 5678", "12345678"} {
		if strings.Contains(unhandled, secret) {
			t.Errorf("the assistant's text holds %q: %s", secret, encoded)
		}
	}
	// The ID number Alice corrected and the one read are two values with a
	// handle each; the text holds the one read.
	number := fieldNamed(t, text, "公民身份号码")
	if !strings.HasPrefix(number.Value, "⟦身份证号·") || !strings.HasPrefix(number.Read, "⟦身份证号·") ||
		number.Value == number.Read || !strings.Contains(text.Text, number.Read) || strings.Contains(text.Text, number.Value) {
		t.Fatalf("公民身份号码 for the assistant = %+v in %q", number, text.Text)
	}
	if name := fieldNamed(t, text, "姓名"); name.Value != "张三" {
		t.Fatalf("姓名 for the assistant = %+v", name)
	}
	if address := fieldNamed(t, text, "住址"); address.Value != "某市某路1号" || address.Sensitive {
		t.Fatalf("住址 for the assistant = %+v", address)
	}
	if !strings.Contains(text.Text, "⟦手机号·") || !strings.Contains(text.Text, "⟦号码·") {
		t.Fatalf("the numbers of the text for the assistant: %q", text.Text)
	}
	// One handle for each value: the ID number, the phone number and the
	// serial number.
	if found := slices.Compact(slices.Sorted(slices.Values(handles.FindAllString(text.Text, -1)))); len(found) != 3 {
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
	for _, query := range []string{"4403", "1236", "1252", "1381234", "12345678901"} {
		if page := search(t, service, alicesAssistant, query); page.Texts != 0 {
			t.Errorf("the assistant's search for %q found the photo by its text", query)
		}
	}
	for _, query := range []string{"张三", "某路"} {
		if page := search(t, service, alicesAssistant, query); page.Texts != 1 {
			t.Errorf("the assistant's search for %q = %+v", query, page)
		}
	}
	if _, err := service.TextChanges(ctx, alicesAssistant, card.ID); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("the assistant's TextChanges(): error = %v", err)
	}
	// Nor does it look at the card: its model would read the numbers.
	if _, err := service.View(ctx, alicesAssistant, card.ID); !errors.Is(err, photos.ErrSensitivePhoto) {
		t.Fatalf("the assistant's View() of the ID card: error = %v", err)
	}
}

// handleOf is the part of a handle the window asks the photo service with.
func handleOf(t *testing.T, named string) string {
	t.Helper()
	found := regexp.MustCompile(`^⟦[^·⟧]+·([0-9a-f]{6})⟧$`).FindStringSubmatch(named)
	if found == nil {
		t.Fatalf("%q is no handle", named)
	}
	return found[1]
}

// The member whose assistant got a handle, and nobody else, gets the value it
// stands for: the window shows it in place of the handle, past the model.
func TestAMemberGetsTheValueAHandleStandsFor(t *testing.T) {
	service, embedder := idCardService(t)
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	libraries(t, service, namedBob)
	card := importPhoto(t, service, namedAlice, shared.ID, "", "card.png", red)
	index(t, service, embedder)
	if _, err := service.CorrectText(ctx, namedAlice, card.ID, "公民身份号码", "440306199001011252"); err != nil {
		t.Fatal(err)
	}
	alicesAssistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}
	bobsAssistant := photos.Principal{UserID: namedBob.UserID, Username: "bob", Assistant: true}
	text, err := service.Text(ctx, alicesAssistant, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	number := fieldNamed(t, text, "公民身份号码")
	inText := handles.FindAllString(text.Text, -1)
	if len(inText) != 3 {
		t.Fatalf("handles in the assistant's text = %v", inText)
	}
	// What the field holds, what was read for it, and the text's numbers in
	// the order the text shows them, as their digits.
	for named, want := range map[string]photos.TextHandleValue{
		number.Value: {Label: "身份证号", Value: "440306199001011252"},
		number.Read:  {Label: "身份证号", Value: "440306199001011236"},
		inText[1]:    {Label: "手机号", Value: "13812345678"},
		inText[2]:    {Label: "号码", Value: "123456789012345"},
	} {
		want.AssetID, want.AssetName = card.ID, "card.png"
		if got, err := service.TextHandle(ctx, namedAlice, handleOf(t, named)); err != nil || got != want {
			t.Errorf("Alice's TextHandle(%s) = %+v, %v; want %+v", named, got, err, want)
		}
	}
	handle := handleOf(t, number.Value)

	// The handle is Alice's: Bob sees the photo and gets nothing by it, nor
	// does any assistant, hers included; Bob's own handle works for him.
	for who, p := range map[string]photos.Principal{"Bob": namedBob, "Alice's assistant": alicesAssistant, "Bob's assistant": bobsAssistant} {
		if _, err := service.TextHandle(ctx, p, handle); !errors.Is(err, photos.ErrNotFound) {
			t.Errorf("%s's TextHandle() of Alice's handle: error = %v", who, err)
		}
	}
	bobs, err := service.Text(ctx, bobsAssistant, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.TextHandle(ctx, namedBob, handleOf(t, fieldNamed(t, bobs, "公民身份号码").Value)); err != nil ||
		got.Value != "440306199001011252" {
		t.Fatalf("Bob's TextHandle() of his own handle = %+v, %v", got, err)
	}
	for _, unknown := range []string{"", "ZZZZZZ", number.Value, handle + "0"} {
		if _, err := service.TextHandle(ctx, namedAlice, unknown); !errors.Is(err, photos.ErrNotFound) {
			t.Errorf("TextHandle(%q): error = %v", unknown, err)
		}
	}

	// A handle follows its field: corrected again, it stands for the new
	// value.
	if _, err := service.CorrectText(ctx, namedBob, card.ID, "公民身份号码", "110101199003074514"); err != nil {
		t.Fatal(err)
	}
	if got, err := service.TextHandle(ctx, namedAlice, handle); err != nil || got.Value != "110101199003074514" {
		t.Fatalf("TextHandle() after a correction = %+v, %v", got, err)
	}
	// With reading off, a library shows no text, and its handles nothing.
	own := importPhoto(t, service, namedAlice, private.ID, "", "own.png", solidPNG(250, 5, 5))
	index(t, service, embedder)
	owned, err := service.Text(ctx, alicesAssistant, own.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownHandle := handleOf(t, fieldNamed(t, owned, "公民身份号码").Value)
	if _, err := service.TextHandle(ctx, namedAlice, ownHandle); err != nil {
		t.Fatalf("TextHandle() of her own photo: %v", err)
	}
	if _, err := service.SetTexts(ctx, namedAlice, private.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TextHandle(ctx, namedAlice, ownHandle); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("TextHandle() with reading off: error = %v", err)
	}
}

// A document is found by what members call its type, which its own text
// need not say: the front of an ID card nowhere prints 身份证.
func TestADocumentIsFoundByTheNameOfItsType(t *testing.T) {
	service, embedder := idCardService(t)
	private, _ := libraries(t, service, namedAlice)
	card := importPhoto(t, service, namedAlice, private.ID, "", "IMG_0001.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, embedder)
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}

	for _, p := range []photos.Principal{namedAlice, assistant} {
		for _, query := range []string{"身份证", "我的身份证号", "证件"} {
			if page := search(t, service, p, query); page.Texts != 1 || page.Assets[0].ID != card.ID {
				t.Errorf("search for %q (assistant: %v) = %+v", query, p.Assistant, page)
			}
		}
		for _, query := range []string{"发票", "银行卡", "身"} {
			if page := search(t, service, p, query); page.Texts != 0 {
				t.Errorf("search for %q (assistant: %v) found the ID card by its text", query, p.Assistant)
			}
		}
	}
}

// An assistant's model is in the cloud for now, so the assistant gets no
// view of a document, whichever side, nor of a photo still waiting to be read
// (docs/decisions/2026-11-10-assistant-view-of-documents.md).
func TestTheAssistantLooksAtNoDocumentAndNoPhotoStillToBeRead(t *testing.T) {
	ctx := context.Background()
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}

	// The back of an ID card shows no number, and is an ID card all the same.
	embedder := newReadingEmbedder()
	embedder.text = "中华人民共和国\n居民身份证\n签发机关 某市公安局\n有效期限 2020.01.01-2040.01.01"
	embedder.answer = `{"签发机关": "某市公安局", "有效期限": "2020.01.01-2040.01.01"}`
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	private, _ := libraries(t, service, namedAlice)
	back := importPhoto(t, service, namedAlice, private.ID, "", "back.png", red)
	index(t, service, embedder)
	text, err := service.Text(ctx, assistant, back.ID)
	if err != nil || text.Kind != "id_card_back" || handles.MatchString(text.Text) {
		t.Fatalf("the back of the ID card for the assistant = %+v, %v", text, err)
	}
	if _, err := service.View(ctx, assistant, back.ID); !errors.Is(err, photos.ErrSensitivePhoto) {
		t.Fatalf("the assistant's View() of the back of an ID card: error = %v", err)
	}

	// A Worker that reads no text leaves the routed photo waiting: nobody
	// knows yet what it shows. The member looks at it as ever.
	waiting := &colorEmbedder{}
	service = routeService(t, filepath.Join(t.TempDir(), "photos"), waiting, redRoute(t, "colour-1"))
	private, _ = libraries(t, service, namedAlice)
	card := importPhoto(t, service, namedAlice, private.ID, "", "card.png", red)
	sea := importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, waiting)
	if _, err := service.View(ctx, assistant, card.ID); !errors.Is(err, photos.ErrUnreadPhoto) {
		t.Fatalf("the assistant's View() of a photo waiting to be read: error = %v", err)
	}
	for who, look := range map[string]func() (photos.Content, error){
		"the assistant's View() of a photo not routed": func() (photos.Content, error) { return service.View(ctx, assistant, sea.ID) },
		"Alice's View() of the photo waiting":          func() (photos.Content, error) { return service.View(ctx, namedAlice, card.ID) },
	} {
		view, err := look()
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		_ = view.Reader.Close()
	}
	// With reading off nothing waits, and the library's photos are looked at
	// as before reading existed.
	if _, err := service.SetTexts(ctx, namedAlice, private.ID, false); err != nil {
		t.Fatal(err)
	}
	view, err := service.View(ctx, assistant, card.ID)
	if err != nil {
		t.Fatalf("the assistant's View() with reading off: %v", err)
	}
	_ = view.Reader.Close()
}

// A number reaches the assistant as a handle wherever the text holds it: in
// a field that is no secret, printed in full-width digits, or split over
// lines, which search would join.
func TestTheAssistantSeesNoNumberHoweverItIsPrinted(t *testing.T) {
	embedder := newReadingEmbedder()
	embedder.text = "城南便利店 电话13812345678\n2024年05月06日\n合计 ¥35.50\n会员 6222 0200\n0000 0000 001\n" +
		"积分卡 ６２２８ ４８００ １２３４ ５６７８ ９０３"
	embedder.answer = `{"商户": "城南便利店 电话13812345678", "日期": "2024年05月06日", "合计": "¥35.50"}`
	root := filepath.Join(t.TempDir(), "photos")
	service := routeService(t, root, embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	receipt := importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	sea := importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, embedder)
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}

	text, err := service.Text(ctx, assistant, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	unhandled := handles.ReplaceAllString(string(encoded), "")
	for _, secret := range []string{"1381234", "6222", "0200", "0000 0000", "６２２８", "6228", "903"} {
		if strings.Contains(unhandled, secret) {
			t.Errorf("the assistant's text holds %q: %s", secret, encoded)
		}
	}
	merchant := fieldNamed(t, text, "商户")
	phone := strings.TrimPrefix(merchant.Value, "城南便利店 电话")
	if !strings.HasPrefix(phone, "⟦手机号·") || merchant.Read != merchant.Value || !strings.Contains(text.Text, phone) {
		t.Fatalf("商户 for the assistant = %+v in %q", merchant, text.Text)
	}
	// Numbers are keyed by their order in the text, never by a digest of
	// theirs that would tell them.
	rows, err := catalogDB(t, root).QueryContext(ctx, "SELECT key FROM text_handles")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keyed := regexp.MustCompile(`^(field:.+|read:.+|number:[0-9]+)$`)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		if !keyed.MatchString(key) {
			t.Errorf("a handle's key %q", key)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Its search finds no part of a number; the member's finds them however
	// they are spaced.
	for _, query := range []string{"1381234", "62220200", "0000 0000", "6228 4800", "６２２８"} {
		if page := search(t, service, assistant, query); page.Texts != 0 {
			t.Errorf("the assistant's search for %q found the photo by its text", query)
		}
		if page := search(t, service, namedAlice, query); page.Texts != 1 {
			t.Errorf("Alice's search for %q = %+v", query, page)
		}
	}
	if page := search(t, service, assistant, "便利店"); page.Texts != 1 {
		t.Errorf("the assistant's search for 便利店 = %+v", page)
	}
	// It looks at a photo with no such number, but not at the receipt.
	if _, err := service.View(ctx, assistant, receipt.ID); !errors.Is(err, photos.ErrSensitivePhoto) {
		t.Fatalf("the assistant's View() of the receipt: error = %v", err)
	}
	view, err := service.View(ctx, assistant, sea.ID)
	if err != nil {
		t.Fatalf("the assistant's View() of the sea: %v", err)
	}
	_ = view.Reader.Close()
	member, err := service.View(ctx, namedAlice, receipt.ID)
	if err != nil {
		t.Fatalf("Alice's View() of the receipt: %v", err)
	}
	_ = member.Reader.Close()
}

// A photo that is no document and whose text shows only a mobile number and
// an address, as a screenshot or a parcel label does, the assistant may look
// at (user, 2026-11-06); the number stays a handle in its text.
func TestTheAssistantLooksAtAPhotoWithOnlyAPhoneNumber(t *testing.T) {
	embedder := newReadingEmbedder()
	embedder.text = "城南便利店 某市某路1号\n电话 138 1234 5678\n营业时间 8:00-22:00"
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	receipt := importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	index(t, service, embedder)
	assistant := photos.Principal{UserID: namedAlice.UserID, Username: "alice", Assistant: true}

	text, err := service.Text(ctx, assistant, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.Text, "某市某路1号") || !strings.Contains(text.Text, "⟦手机号·") || strings.Contains(text.Text, "1381234") {
		t.Fatalf("the assistant's text = %q", text.Text)
	}
	view, err := service.View(ctx, assistant, receipt.ID)
	if err != nil {
		t.Fatalf("the assistant's View() of the receipt: %v", err)
	}
	_ = view.Reader.Close()
}

func TestTextMatchesFollowNamesAndCountOnEveryPage(t *testing.T) {
	embedder := newReadingEmbedder()
	root := filepath.Join(t.TempDir(), "photos")
	service := routeService(t, root, embedder, redRoute(t, "colour-1"))
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
	if len(page.Assets) != 1 || page.Assets[0].Name != "超市.png" || page.Names != 1 || page.Texts != 1 || page.Closest != 3 || page.Next == "" {
		t.Fatalf("first page = %+v", page)
	}
	page, err = service.Search(ctx, namedAlice, photos.SearchRequest{Query: "超市", Limit: 1, Cursor: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Assets) != 1 || page.Assets[0].ID != receipt.ID || page.Texts != 1 {
		t.Fatalf("second page = %+v", page)
	}
	// One character finds no text: nearly every text holds a common one.
	if page := search(t, service, namedAlice, "超"); page.Texts != 0 {
		t.Fatalf("search for 超 = %+v", page)
	}
	// A correction the text does not show, of a field another document type
	// has, finds nothing either.
	db := catalogDB(t, root)
	if _, err := db.ExecContext(ctx, `
INSERT INTO text_corrections(object_id, field, value, account, account_name, updated_at)
SELECT object_id, '卡号', '旧类型的更正', 'user:alice', 'alice', '2026-11-06T00:00:00.000000000Z' FROM assets WHERE id = ?`,
		receipt.ID); err != nil {
		t.Fatal(err)
	}
	if page := search(t, service, namedAlice, "旧类型的更正"); page.Texts != 0 {
		t.Fatalf("search for a correction the text does not show = %+v", page)
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
	if details, err := service.Get(ctx, namedAlice, receipt.ID); err != nil || details.Text != "" {
		t.Fatalf("the details with reading off: Text = %q, %v", details.Text, err)
	}
}
