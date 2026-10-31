package analytics

import (
	"slices"
	"strings"
)

type configLocation struct {
	Name string `json:"name"`
}

// locations creates the locations a file names that do not exist yet,
// without keys: each needs one made in the Locations tab before it runs.
func (ci *configImport) locations(doc configDoc) error {
	for _, in := range doc.UptimeLocations {
		name := strings.ToLower(strings.TrimSpace(in.Name))
		if name == serverLocation || slices.ContainsFunc(ci.st.locations, func(l uptimeLocation) bool { return l.Name == name }) {
			ci.note("uptime_location", name, []string{})
			continue
		}
		if !locationNameRe.MatchString(name) {
			ci.skip("uptime_location", in.Name, "not a valid location name")
			continue
		}
		ci.note("uptime_location", name, nil)
		ci.warn("make its key under Uptime → Locations, then start its runner")
		if ci.apply {
			if _, _, err := ci.s.createLocation(ci.r, name, false); err != nil {
				return err
			}
		}
		ci.st.locations = append(ci.st.locations, uptimeLocation{Name: name})
	}
	return nil
}

func (ci *configImport) missingLocation(c UptimeCheck) string {
	for _, name := range c.Locations {
		if name != serverLocation && !slices.ContainsFunc(ci.st.locations, func(l uptimeLocation) bool { return l.Name == name }) {
			return name
		}
	}
	return ""
}
