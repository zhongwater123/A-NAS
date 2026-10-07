package photos_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

type policyFixture struct {
	service *photos.Service
	asset   photos.Asset
}

type policyOperation struct {
	name string
	// trashFirst has the uploader trash the asset before the operation runs.
	trashFirst bool
	run        func(context.Context, policyFixture, photos.Principal) error
}

var policyOperations = []policyOperation{
	{name: "get", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Get(ctx, p, f.asset.ID)
		return err
	}},
	{name: "open", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		content, err := f.service.Open(ctx, p, f.asset.ID)
		if err == nil {
			_ = content.Reader.Close()
		}
		return err
	}},
	{name: "timeline", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Timeline(ctx, p, f.asset.LibraryID, "", 10)
		return err
	}},
	{name: "import", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Import(ctx, p, photos.ImportRequest{
			LibraryID: f.asset.LibraryID, Name: "new.png", Content: bytes.NewReader(encodePNG(200)),
		})
		return err
	}},
	{name: "rename", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Rename(ctx, p, f.asset.ID, "renamed.png")
		return err
	}},
	{name: "trash", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Trash(ctx, p, f.asset.ID)
		return err
	}},
	{name: "restore", trashFirst: true, run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		_, err := f.service.Restore(ctx, p, f.asset.ID)
		return err
	}},
	{name: "purge", trashFirst: true, run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		return f.service.Purge(ctx, p, f.asset.ID)
	}},
	{name: "copy-to-own-library", run: func(ctx context.Context, f policyFixture, p photos.Principal) error {
		all, err := f.service.Libraries(ctx, p)
		if err != nil {
			return err
		}
		for _, lib := range all {
			if lib.Kind == photos.LibraryKindPrivate && lib.OwnerUserID == p.UserID {
				_, err = f.service.Copy(ctx, p, f.asset.ID, lib.ID, "")
				return err
			}
		}
		return errors.New("no private library")
	}},
}

func TestPolicyMatrix(t *testing.T) {
	now := newClock().Now()
	viewer := photos.Principal{UserID: admin.UserID, Admin: true, Viewing: []photos.ViewingGrant{
		{OwnerUserID: alice.UserID, ExpiresAt: now.Add(time.Hour)},
	}}
	expiredViewer := photos.Principal{UserID: admin.UserID, Admin: true, Viewing: []photos.ViewingGrant{
		{OwnerUserID: alice.UserID, ExpiresAt: now},
	}}
	// memberViewer shows that a grant means nothing without the admin role.
	memberViewer := photos.Principal{UserID: bob.UserID, Viewing: viewer.Viewing}
	principals := []struct {
		name string
		p    photos.Principal
	}{
		{"owner", alice}, {"member", bob}, {"admin", admin}, {"viewing-admin", viewer},
		{"expired-viewing-admin", expiredViewer}, {"member-with-grant", memberViewer},
	}

	ok, notFound, forbidden := error(nil), photos.ErrNotFound, photos.ErrForbidden
	want := map[string]map[string][]error{
		// Order follows principals: owner, member, admin, viewing admin,
		// expired viewing admin, member with a grant.
		"private": {
			"get":                 {ok, notFound, notFound, ok, notFound, notFound},
			"open":                {ok, notFound, notFound, ok, notFound, notFound},
			"timeline":            {ok, notFound, notFound, ok, notFound, notFound},
			"import":              {ok, notFound, notFound, forbidden, notFound, notFound},
			"rename":              {ok, notFound, notFound, forbidden, notFound, notFound},
			"trash":               {ok, notFound, notFound, forbidden, notFound, notFound},
			"restore":             {ok, notFound, notFound, notFound, notFound, notFound},
			"purge":               {ok, notFound, notFound, notFound, notFound, notFound},
			"copy-to-own-library": {ok, notFound, notFound, ok, notFound, notFound},
		},
		// The shared asset was uploaded by the owner principal (alice).
		"shared": {
			"get":                 {ok, ok, ok, ok, ok, ok},
			"open":                {ok, ok, ok, ok, ok, ok},
			"timeline":            {ok, ok, ok, ok, ok, ok},
			"import":              {ok, ok, ok, ok, ok, ok},
			"rename":              {ok, forbidden, ok, ok, ok, forbidden},
			"trash":               {ok, forbidden, ok, ok, ok, forbidden},
			"restore":             {ok, notFound, ok, ok, ok, notFound},
			"purge":               {ok, notFound, ok, ok, ok, notFound},
			"copy-to-own-library": {ok, ok, ok, ok, ok, ok},
		},
	}

	for _, target := range []string{"private", "shared"} {
		for _, operation := range policyOperations {
			for i, principal := range principals {
				t.Run(target+"/"+operation.name+"/"+principal.name, func(t *testing.T) {
					ctx := context.Background()
					service, _, _ := newService(t)
					alicePrivate, shared := libraries(t, service, alice)
					libraryID := alicePrivate.ID
					if target == "shared" {
						libraryID = shared.ID
					}
					asset := importPhoto(t, service, alice, libraryID, "", "photo.png", pngBytes(t, 1))
					if operation.trashFirst {
						if _, err := service.Trash(ctx, alice, asset.ID); err != nil {
							t.Fatalf("Trash() error = %v", err)
						}
					}
					got := operation.run(ctx, policyFixture{service: service, asset: asset}, principal.p)
					if expected := want[target][operation.name][i]; !errors.Is(got, expected) || (expected == nil && got != nil) {
						t.Fatalf("error = %v, want %v", got, expected)
					}
				})
			}
		}
	}
}

func TestHiddenAssetIsIndistinguishableFromMissing(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	alicePrivate, _ := libraries(t, service, alice)
	asset := importPhoto(t, service, alice, alicePrivate.ID, "", "secret.png", pngBytes(t, 2))

	_, hiddenErr := service.Get(ctx, bob, asset.ID)
	_, missingErr := service.Get(ctx, bob, "photo:does-not-exist")
	if hiddenErr == nil || hiddenErr.Error() != missingErr.Error() {
		t.Fatalf("hidden error %v differs from missing error %v", hiddenErr, missingErr)
	}
	_, hiddenErr = service.ListDirectory(ctx, bob, alicePrivate.ID, "")
	_, missingErr = service.ListDirectory(ctx, bob, "library:does-not-exist", "")
	if hiddenErr == nil || hiddenErr.Error() != missingErr.Error() {
		t.Fatalf("hidden library error %v differs from missing error %v", hiddenErr, missingErr)
	}
}

func TestViewingGrantListsMemberLibraryReadOnlyUntilItExpires(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, _ := libraries(t, service, alice)
	viewer := photos.Principal{UserID: admin.UserID, Admin: true, Viewing: []photos.ViewingGrant{
		{OwnerUserID: alice.UserID, ExpiresAt: c.Now().Add(24 * time.Hour)},
		{OwnerUserID: bob.UserID, ExpiresAt: c.Now().Add(24 * time.Hour)},
	}}

	all, err := service.Libraries(ctx, viewer)
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	var viewed []photos.Library
	for _, lib := range all {
		if lib.Viewing {
			viewed = append(viewed, lib)
		}
	}
	// Bob has never opened the photo library, so there is nothing to view.
	if len(viewed) != 1 || viewed[0].ID != alicePrivate.ID {
		t.Fatalf("viewed libraries = %+v, want only alice's", viewed)
	}

	c.Advance(24 * time.Hour)
	all, err = service.Libraries(ctx, viewer)
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	for _, lib := range all {
		if lib.Viewing {
			t.Fatalf("expired grant still lists %+v", lib)
		}
	}
	if _, err := service.Timeline(ctx, viewer, alicePrivate.ID, "", 10); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Timeline() after expiry error = %v, want ErrNotFound", err)
	}
}

func TestSharedDirectoriesBelongToTheirCreator(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	_, shared := libraries(t, service, alice)
	directory, err := service.CreateDirectory(ctx, alice, shared.ID, "", "Trip")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	if _, err := service.RenameDirectory(ctx, bob, directory.ID, "Bob's"); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("member RenameDirectory() error = %v, want ErrForbidden", err)
	}
	// Any member may still file their own photos into it.
	asset := importPhoto(t, service, bob, shared.ID, directory.ID, "bob.png", pngBytes(t, 3))
	if asset.DirectoryID != directory.ID {
		t.Fatalf("DirectoryID = %q, want %q", asset.DirectoryID, directory.ID)
	}
	if _, err := service.RenameDirectory(ctx, admin, directory.ID, "Family trip"); err != nil {
		t.Fatalf("admin RenameDirectory() error = %v", err)
	}
}

func TestPrincipalWithoutUserIsRejected(t *testing.T) {
	service, _, _ := newService(t)
	if _, err := service.Libraries(context.Background(), photos.Principal{}); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("Libraries() error = %v, want ErrForbidden", err)
	}
}
