package familyapi

import (
	"net/url"
	"testing"
)

// A screen's report is read field by field; a malformed field is left out
// and never fails the question.
func TestDeviceReport(t *testing.T) {
	query, _ := url.ParseQuery("bat=35&mv=bad&chg=1&usb=2&fw=0.2.5-s3&rssi=-61&awake_ms=4294967295&wake=key&reset=Brown" +
		"&fail=cap:next&hist=0712a-none-4.0715k-wifi-11&bat=99")
	report := deviceReport(query)
	if report.Battery == nil || *report.Battery != 35 || report.Millivolt != nil || report.Charging == nil || !*report.Charging ||
		report.USB != nil || report.Firmware != "0.2.5-s3" || report.RSSI == nil || *report.RSSI != -61 ||
		report.AwakeMs == nil || *report.AwakeMs != 4294967295 || report.Wake != "key" || report.Reset != "" ||
		report.Fail != "cap:next" || report.History != "0712a-none-4.0715k-wifi-11" {
		t.Fatalf("report = %+v", report)
	}
	for _, bad := range []string{"bat=101", "bat=-1", "rssi=5", "awake_ms=4294967296", "fw=<b>", "hist=0712a none", "fail=" +
		"abcdefghijklmnopqrstuvwx"} {
		query, _ := url.ParseQuery(bad)
		if report := deviceReport(query); report.Battery != nil || report.RSSI != nil || report.AwakeMs != nil ||
			report.Firmware != "" || report.History != "" || report.Fail != "" {
			t.Errorf("%s was kept: %+v", bad, report)
		}
	}
}
