package httpapi_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/openapitest"
)

// TestMain reports, after a run of every test, the operations of the
// contract that no checked exchange answered with a 2xx status. The report
// does not fail the run yet (#273). `go test` shows it when run in this
// directory without a package argument, or with -v.
func TestMain(m *testing.M) {
	code := m.Run()
	if run := flag.Lookup("test.run"); code == 0 && (run == nil || run.Value.String() == "") {
		missing, err := openapitest.WithoutSuccess()
		if err != nil {
			fmt.Println("contract:", err)
			os.Exit(1)
		}
		if len(missing) > 0 {
			fmt.Printf("contract: %d operations answered no checked 2xx:\n  %s\n", len(missing), strings.Join(missing, "\n  "))
		}
	}
	os.Exit(code)
}
