package metrics

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

var seriesRe = regexp.MustCompile(`csi_rclone_[a-z_]+`)

// TestDashboardUsesDocumentedSeries keeps the Grafana dashboard valid JSON and in
// step with docs/reference/metrics.md.
func TestDashboardUsesDocumentedSeries(t *testing.T) {
	raw, err := os.ReadFile("../../charts/csi-driver-rclone/dashboards/csi-driver-rclone.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UID    string `json:"uid"`
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("dashboard is not JSON: %v", err)
	}
	doc, err := os.ReadFile("../../docs/reference/metrics.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.UID == "" || len(d.Panels) == 0 {
		t.Fatal("dashboard without uid or panels")
	}
	for _, p := range d.Panels {
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no query", p.Title)
		}
		for _, tg := range p.Targets {
			for _, s := range seriesRe.FindAllString(tg.Expr, -1) {
				s = strings.TrimSuffix(s, "_bucket")
				if !strings.Contains(string(doc), "`"+s+"`") {
					t.Errorf("panel %q uses %s, not in docs/reference/metrics.md", p.Title, s)
				}
			}
		}
	}
}
