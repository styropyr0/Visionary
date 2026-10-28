package analytics

import (
	"context"
	"fmt"
	"time"
)

type digestLogs struct {
	Lines      int64              `json:"lines"`
	PrevLines  int64              `json:"previous_lines"`
	Errors     int64              `json:"error_lines"`
	PrevErrors int64              `json:"previous_error_lines"`
	New        []digestLogMessage `json:"new_messages"`
}

type digestLogMessage struct {
	Service  string `json:"service"`
	Level    string `json:"level"`
	Template string `json:"template"`
	Lines    int64  `json:"lines"`
}

func (s *Server) digestLogsData(ctx context.Context, start, mid, now time.Time) *digestLogs {
	week := func(from, to time.Time) (int64, int64, bool) {
		res, err := s.db.Query(ctx, `SELECT COUNT(*), `+countIf("severity >= 5")+` FROM app_logs WHERE created_at >= ? AND created_at < ?`,
			from.UTC().Format(tsLayout), to.UTC().Format(tsLayout))
		if err != nil || len(res.Rows) == 0 {
			return 0, 0, false
		}
		return toInt(res.Rows[0][0]), toInt(res.Rows[0][1]), true
	}
	lines, errs, ok := week(mid, now)
	if !ok {
		return nil
	}
	prevLines, prevErrs, _ := week(start, mid)
	if lines == 0 && prevLines == 0 {
		return nil
	}
	d := &digestLogs{Lines: lines, PrevLines: prevLines, Errors: errs, PrevErrors: prevErrs}
	if res, err := s.db.Query(ctx, `SELECT service, level, template, lines FROM log_patterns WHERE first_seen >= ? AND first_seen < ? AND severity >= 4
ORDER BY severity DESC, lines DESC LIMIT 8`, mid.UTC().Format(tsLayout), now.UTC().Format(tsLayout)); err == nil {
		for _, row := range res.Rows {
			d.New = append(d.New, digestLogMessage{Service: toString(row[0]), Level: toString(row[1]), Template: toString(row[2]), Lines: toInt(row[3])})
		}
	}
	return d
}

func digestLogsSection(l *digestLogs) emailSection {
	sec := emailSection{
		Title: "Logs",
		Note: fmt.Sprintf("%s lines (%s the week before), %s at error or worse (%s).",
			groupNum(float64(l.Lines)), groupNum(float64(l.PrevLines)), groupNum(float64(l.Errors)), groupNum(float64(l.PrevErrors))),
		Columns: []string{"New message", "Service", "Level", "Lines"},
	}
	for _, m := range l.New {
		tone := ""
		if m.Level == "error" || m.Level == "fatal" {
			tone = "bad"
		}
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{truncate(m.Template, 90), m.Service, m.Level, groupNum(float64(m.Lines))}, Tone: tone})
	}
	return sec
}
