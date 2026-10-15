// Command photos-spike runs the A-NAS photo service for the assistant spike
// (#113, docs/research/hermes-agent-runtime.md): the real photoservice.Run
// with its assistant socket, a small library for alice and bob, and a
// stand-in for the AI Worker that tells photos apart by their colour.
//
//	photos-spike STATE_DIR PHOTO_SOCKET ASSISTANT_SOCKET
//
// It seeds the library through the session socket, waits until every photo
// has its vector, prints "photos-ready" and keeps serving. Accounts come from
// the container's passwd and group files: a member of a-nas-users is
// "user:<name>", where A-NAS asks the Host Agent.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
)

// concept is what a fixture photo shows. The stand-in Worker sees only the
// colour each one is painted in, and reads a query by these words.
type concept struct {
	colour color.RGBA
	words  []string
}

var concepts = []concept{
	{color.RGBA{30, 120, 220, 255}, []string{"海边", "海滩", "沙滩", "大海", "海", "游泳", "beach", "sea"}},
	{color.RGBA{240, 240, 250, 255}, []string{"雪", "滑雪", "snow"}},
	{color.RGBA{200, 140, 60, 255}, []string{"猫", "cat"}},
	{color.RGBA{230, 60, 120, 255}, []string{"生日", "蛋糕", "birthday", "cake"}},
	{color.RGBA{60, 140, 60, 255}, []string{"山", "爬山", "登山", "mountain"}},
	{color.RGBA{250, 200, 160, 255}, []string{"合照", "合影", "全家福", "人", "家人", "妈妈", "爸爸", "people"}},
	{color.RGBA{150, 70, 30, 255}, []string{"美食", "吃", "饭", "火锅", "food"}},
}

const (
	beach = iota
	snow
	cat
	birthday
	mountain
	people
	food
)

type fixture struct {
	owner, library, name string
	shows                int
	taken                string // EXIF DateTimeOriginal; empty for none
	tags                 []string
	album                string
}

// "去年" is 2025 in the spike's October 2026.
var fixtures = []fixture{
	{"alice", "private", "IMG_3101.jpg", beach, "2025:07:12 16:20:00", []string{"青岛"}, "2025 夏天 青岛"},
	{"alice", "private", "IMG_3102.jpg", beach, "2025:07:12 17:05:00", []string{"青岛"}, "2025 夏天 青岛"},
	{"alice", "private", "IMG_3115.jpg", people, "2025:07:13 10:30:00", []string{"青岛"}, "2025 夏天 青岛"},
	{"alice", "private", "IMG_2801.jpg", beach, "2024:08:03 15:00:00", nil, ""},
	{"alice", "private", "IMG_2802.jpg", beach, "2024:08:03 15:40:00", nil, ""},
	{"alice", "private", "IMG_3010.jpg", snow, "2025:01:18 09:00:00", nil, ""},
	{"alice", "private", "IMG_3011.jpg", snow, "2025:01:18 09:20:00", nil, ""},
	{"alice", "private", "IMG_3301.jpg", cat, "2025:05:02 20:10:00", nil, ""},
	{"alice", "private", "IMG_3302.jpg", cat, "2025:11:09 21:00:00", []string{"咪咪"}, ""},
	{"alice", "private", "IMG_3405.jpg", birthday, "2025:10:20 19:30:00", nil, ""},
	{"alice", "private", "IMG_3520.jpg", mountain, "2026:02:14 11:00:00", nil, ""},
	{"alice", "private", "IMG_3601.jpg", people, "2026:03:08 12:00:00", nil, ""},
	{"alice", "private", "扫描件.jpg", food, "", nil, ""},
	{"bob", "private", "IMG_9001.jpg", beach, "2025:07:20 14:00:00", nil, ""},
	{"bob", "private", "IMG_9002.jpg", people, "2025:07:20 14:10:00", nil, ""},
	{"bob", "shared", "全家福_2025春节.jpg", people, "2025:01:29 12:00:00", nil, ""},
	{"bob", "shared", "IMG_7001.jpg", beach, "2025:08:15 18:00:00", nil, ""},
	{"bob", "shared", "IMG_7002.jpg", food, "2025:12:31 20:00:00", nil, ""},
}

// colours is the stand-in AI Worker: an image's vector names the concept
// whose colour is nearest its mean colour, and a query's vector the concepts
// it names. A query naming none points nowhere, so every photo is as far.
type colours struct{}

func (colours) Info(context.Context) (photos.EmbedderInfo, error) {
	return photos.EmbedderInfo{Model: "spike-colours-1", Dimensions: len(concepts) + 1}, nil
}

func (colours) EmbedImage(_ context.Context, file *os.File) ([]float32, error) {
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, photos.ErrAIRejected
	}
	var r, g, b, n float64
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r, g, b, n = r+float64(cr>>8), g+float64(cg>>8), b+float64(cb>>8), n+1
		}
	}
	best, distance := 0, math.MaxFloat64
	for i, c := range concepts {
		d := math.Pow(r/n-float64(c.colour.R), 2) + math.Pow(g/n-float64(c.colour.G), 2) + math.Pow(b/n-float64(c.colour.B), 2)
		if d < distance {
			best, distance = i, d
		}
	}
	vector := make([]float32, len(concepts)+1)
	vector[best] = 1
	return vector, nil
}

func (colours) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	vector := make([]float32, len(concepts)+1)
	var sum float32
	for i, c := range concepts {
		if slices.ContainsFunc(c.words, func(word string) bool { return strings.Contains(strings.ToLower(text), word) }) {
			vector[i], sum = 1, sum+1
		}
	}
	if sum == 0 {
		vector[len(concepts)], sum = 1, 1
	}
	norm := float32(math.Sqrt(float64(sum)))
	for i := range vector {
		vector[i] /= norm
	}
	return vector, nil
}

// passwdAccounts answers as the Host Agent would for the spike's members.
type passwdAccounts struct{}

func (passwdAccounts) ResolveAccountUID(_ context.Context, uid int) (accounts.SessionUser, error) {
	member, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return accounts.SessionUser{}, accounts.ErrUserNotFound
	}
	group, err := user.LookupGroup("a-nas-users")
	if err != nil {
		return accounts.SessionUser{}, err
	}
	groups, err := member.GroupIds()
	if err != nil || !slices.Contains(groups, group.Gid) {
		return accounts.SessionUser{}, accounts.ErrUserNotFound
	}
	return accounts.SessionUser{UserID: "user:" + member.Username, Username: member.Username, Role: accounts.RoleMember}, nil
}

// seedSessions are the seeding program's own sessions, for the photo socket.
type seedSessions struct{}

func (seedSessions) ResolveSessionUser(_ context.Context, token string) (accounts.SessionUser, error) {
	name, ok := strings.CutPrefix(token, "seed-")
	if !ok || (name != "alice" && name != "bob") {
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	}
	return accounts.SessionUser{UserID: "user:" + name, Username: name, Role: accounts.RoleMember}, nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: photos-spike STATE_DIR PHOTO_SOCKET ASSISTANT_SOCKET")
		os.Exit(2)
	}
	state, photoSocket, assistantSocket := os.Args[1], os.Args[2], os.Args[3]
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- photoservice.Run(ctx, photoservice.Config{
			Root: filepath.Join(state, "photos"), SocketPath: photoSocket, Sessions: seedSessions{},
			AssistantSocketPath: assistantSocket, Accounts: passwdAccounts{}, Logger: logger,
			// The spike's store is a plain directory, not the Btrfs data volume.
			CheckStore: func(string) error { return nil },
			Photos:     photos.Options{DisableCapacityReserve: true},
			AI:         colours{}, AIQuietPeriod: 500 * time.Millisecond,
			AIPressure: func(string) (float64, error) { return 0, nil },
		})
	}()
	if err := seed(ctx, photoSocket); err != nil {
		logger.Error("seeding failed", "error", err)
		os.Exit(1)
	}
	fmt.Println("photos-ready")
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("photo service stopped", "error", err)
		os.Exit(1)
	}
}

type api struct {
	client *http.Client
}

func (a api) call(ctx context.Context, member, method, path string, body io.Reader, contentType string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://photos"+photosapi.PathPrefix+path, body)
	if err != nil {
		return err
	}
	request.Header.Set(photosapi.SessionHeader, "seed-"+member)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		detail, _ := io.ReadAll(response.Body)
		return fmt.Errorf("%s %s: %s %s", method, path, response.Status, detail)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (a api) jsonCall(ctx context.Context, member, method, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return a.call(ctx, member, method, path, bytes.NewReader(encoded), "application/json", out)
}

// seed fills an empty library and waits for every vector; a library seeded
// before is left as it is.
func seed(ctx context.Context, socket string) error {
	a := api{client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
	libraries := map[string]map[string]string{}
	for _, member := range []string{"alice", "bob"} {
		var list struct{ Items []photos.Library }
		for attempt := 0; ; attempt++ {
			err := a.call(ctx, member, http.MethodGet, "/libraries", nil, "", &list)
			if err == nil {
				break
			}
			if attempt > 300 {
				return err
			}
			time.Sleep(100 * time.Millisecond)
		}
		libraries[member] = map[string]string{}
		for _, lib := range list.Items {
			libraries[member][string(lib.Kind)] = lib.ID
		}
	}
	var existing struct{ Items []photos.Asset }
	if err := a.call(ctx, "alice", http.MethodGet, "/libraries/"+libraries["alice"]["private"]+"/timeline", nil, "", &existing); err != nil {
		return err
	}
	if len(existing.Items) == 0 {
		albums := map[string]string{}
		for _, f := range fixtures {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			part, err := form.CreateFormFile("file", f.name)
			if err != nil {
				return err
			}
			if _, err := part.Write(photo(concepts[f.shows].colour, f.taken)); err != nil {
				return err
			}
			if err := form.Close(); err != nil {
				return err
			}
			var asset photos.Asset
			library := libraries[f.owner][f.library]
			if err := a.call(ctx, f.owner, http.MethodPost, "/libraries/"+library+"/uploads", &body, form.FormDataContentType(), &asset); err != nil {
				return err
			}
			for _, tag := range f.tags {
				if err := a.jsonCall(ctx, f.owner, http.MethodPost, "/assets/"+asset.ID+"/tags", map[string]string{"name": tag}, nil); err != nil {
					return err
				}
			}
			if f.album == "" {
				continue
			}
			if albums[f.album] == "" {
				var album photos.Album
				if err := a.jsonCall(ctx, f.owner, http.MethodPost, "/libraries/"+library+"/albums", map[string]string{"name": f.album}, &album); err != nil {
					return err
				}
				albums[f.album] = album.ID
			}
			if err := a.jsonCall(ctx, f.owner, http.MethodPost, "/albums/"+albums[f.album]+"/assets", map[string]string{"assetId": asset.ID}, nil); err != nil {
				return err
			}
		}
	}
	deadline := time.Now().Add(3 * time.Minute)
	for _, member := range []string{"alice", "bob"} {
		for {
			var status photos.AIStatus
			if err := a.call(ctx, member, http.MethodGet, strings.TrimPrefix(photosapi.AIStatusPath, photosapi.PathPrefix), nil, "", &status); err != nil {
				return err
			}
			if status.Pending == 0 && status.Ready > 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("vectors for %s still pending: %+v", member, status)
			}
			time.Sleep(time.Second)
		}
	}
	return nil
}

// photo paints a small JPEG in one colour and records when it was taken.
func photo(c color.RGBA, taken string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for x := range 96 {
		for y := range 64 {
			img.Set(x, y, c)
		}
	}
	var encoded bytes.Buffer
	_ = jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90})
	if taken == "" {
		return encoded.Bytes()
	}
	// Little-endian TIFF: IFD0 with the Exif IFD pointer, then the Exif IFD
	// with DateTimeOriginal, then its string.
	le := binary.LittleEndian
	var tiff bytes.Buffer
	write := func(values ...any) {
		for _, value := range values {
			_ = binary.Write(&tiff, le, value)
		}
	}
	const ifd0 = 8
	const exifIFD = ifd0 + 2 + 12 + 4
	const text = exifIFD + 2 + 12 + 4
	write([]byte("II"), uint16(42), uint32(ifd0))
	write(uint16(1), uint16(0x8769), uint16(4), uint32(1), uint32(exifIFD), uint32(0))
	write(uint16(1), uint16(0x9003), uint16(2), uint32(len(taken)+1), uint32(text), uint32(0))
	tiff.WriteString(taken + "\x00")
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write(encoded.Bytes()[:2])
	out.Write([]byte{0xff, 0xe1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(app1)+2))
	out.Write(app1)
	out.Write(encoded.Bytes()[2:])
	return out.Bytes()
}
