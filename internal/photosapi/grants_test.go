package photosapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// A full page of grants fits in one answer even when every grant has the
// longest project ID allowed, made of bytes JSON writes as six.
func TestAFullPageOfGrantsFitsInOneAnswer(t *testing.T) {
	grants := make([]photos.Grant, photosapi.GrantListPage)
	for i := range grants {
		grants[i] = photos.Grant{
			ID:          fmt.Sprintf("grant:%064x", i),
			OwnerUserID: fmt.Sprintf("user:%064x", i),
			ProjectID:   strings.Repeat("\x01", 128),
		}
	}
	answer := photosapi.GrantResponse{OK: true, Grants: grants, Next: grants[len(grants)-1].ID}
	if _, err := unixmsg.Encode(answer, photosapi.GrantMessageLimit); err != nil {
		t.Fatalf("a full page does not fit: %v", err)
	}
}
