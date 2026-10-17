package analytics

import (
	"slices"
	"strings"
)

// Viz tells the frontend how to draw a result.
type Viz struct {
	Type   string   `json:"type"`
	X      string   `json:"x,omitempty"`
	Y      []string `json:"y,omitempty"`
	Series string   `json:"series,omitempty"`
	Stack  bool     `json:"stack,omitempty"`
	Unit   string   `json:"unit,omitempty"`
}

var (
	vizTypes = []string{"timeseries", "bar", "pie", "stat", "table"}
	vizUnits = []string{"count", "ms", "percent", "bytes", "none"}
)

// normalizeViz keeps a chart choice only when it fits the columns the query
// actually returned; otherwise the result is shown as a table.
func normalizeViz(v Viz, columns []string) Viz {
	v.Type = strings.ToLower(strings.TrimSpace(v.Type))
	if !slices.Contains(vizTypes, v.Type) {
		v.Type = "table"
	}
	v.Unit = strings.ToLower(strings.TrimSpace(v.Unit))
	if !slices.Contains(vizUnits, v.Unit) {
		v.Unit = "none"
	}
	has := func(c string) bool { return slices.Contains(columns, c) }

	var ys []string
	for _, y := range v.Y {
		if has(y) {
			ys = append(ys, y)
		}
	}
	v.Y = ys
	if v.Series != "" && !has(v.Series) {
		v.Series = ""
	}
	switch v.Type {
	case "timeseries", "bar", "pie":
		if !has(v.X) || len(v.Y) == 0 {
			v.Type = "table"
		}
	case "stat":
		if len(v.Y) == 0 && len(columns) > 0 {
			v.Y = []string{columns[0]}
		}
	}
	return v
}

func fallback(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
