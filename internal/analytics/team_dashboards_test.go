package analytics

import (
	"regexp"
	"slices"
	"testing"
)

func TestChartRefs(t *testing.T) {
	ok := func(ref string) bool {
		return slices.ContainsFunc(chartRefs, func(re *regexp.Regexp) bool { return re.MatchString(ref) })
	}
	for _, ref := range []string{"containers/panels/cpu", "security/panels/failed_logins", "rum/sessions", "uptime/checks/12/latency", "telemetry/errors", "insights/dependencies"} {
		if !ok(ref) {
			t.Errorf("%q should be accepted", ref)
		}
	}
	for _, ref := range []string{"team/keys", "rum/../team/keys", "uptime/checks/x/latency", "telemetry/errors/abc", "settings/server", "containers/panels/"} {
		if ok(ref) {
			t.Errorf("%q should be rejected", ref)
		}
	}
}

func TestValidatePanelSource(t *testing.T) {
	good := dashPanel{Params: map[string]string{"host": "web-1", "container": "api"}, Field: "timeline", Viz: []byte(`{"type":"timeseries"}`)}
	if err := validatePanelSource(&good); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]dashPanel{
		"range param":   {Params: map[string]string{"from": "2026-01-01"}},
		"bad key":       {Params: map[string]string{"Host": "x"}},
		"control char":  {Params: map[string]string{"host": "a\nb"}},
		"bad field":     {Field: "time-line"},
		"oversized viz": {Viz: make([]byte, maxVizSize+1)},
	} {
		if validatePanelSource(&p) == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
