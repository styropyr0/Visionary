package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) applogsAIRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/applogs/ask", s.authed(s.handleLogAsk))
	mux.Handle("POST /api/applogs/explain", s.authed(s.handleLogExplain))
}

const logSearchHelp = `The search language:
- words and "quoted phrases" match the message (any case)
- field:value matches a column or a field: level, service, source, host, container, logger, version, request (request id), trace, user, session, pattern, file, line, or any field of the line by its name (order_id:42, http.status:500)
- level:error+ (or level:>=error) is that level or worse; levels are trace, debug, info, warn, error, fatal
- field:val* for prefixes, field:* for "is set", field:>=500 for numbers
- -term or NOT term to exclude, OR between alternatives, (parentheses) to group; terms side by side must all match
- /regex/ for a regular expression on the message`

// logFieldsHint lists services and common fields, so the model uses real names.
func (s *Server) logFieldsHint(ctx context.Context) string {
	since := time.Now().UTC().Add(-24 * time.Hour).Format(tsLayout)
	var parts []string
	if res, err := s.db.Query(ctx, `SELECT service, COUNT(*) FROM app_logs WHERE created_at >= ? AND service IS NOT NULL GROUP BY service ORDER BY 2 DESC LIMIT 20`, since); err == nil {
		var names []string
		for _, r := range res.Rows {
			names = append(names, toString(r[0]))
		}
		if len(names) > 0 {
			parts = append(parts, "Services: "+strings.Join(names, ", "))
		}
	}
	if res, err := s.db.Query(ctx, `SELECT attrs FROM app_logs WHERE created_at >= ? AND attrs IS NOT NULL ORDER BY id DESC LIMIT 300`, since); err == nil {
		seen := map[string]bool{}
		var keys []string
		for _, r := range res.Rows {
			var a map[string]any
			if json.Unmarshal([]byte(toString(r[0])), &a) == nil {
				for k := range a {
					if !seen[k] && len(keys) < 40 {
						seen[k] = true
						keys = append(keys, k)
					}
				}
			}
		}
		if len(keys) > 0 {
			parts = append(parts, "Fields seen recently: "+strings.Join(keys, ", "))
		}
	}
	if res, err := s.db.Query(ctx, `SELECT service, version, MIN(created_at) FROM releases GROUP BY service, version ORDER BY 3 DESC LIMIT 5`); err == nil && len(res.Rows) > 0 {
		var rel []string
		for _, r := range res.Rows {
			rel = append(rel, fmt.Sprintf("%s %s at %s", toString(r[0]), toString(r[1]), toString(r[2])))
		}
		parts = append(parts, "Latest deploys: "+strings.Join(rel, "; "))
	}
	return strings.Join(parts, "\n")
}

func (s *Server) handleLogAsk(w http.ResponseWriter, r *http.Request) {
	if s.llm() == nil {
		writeError(w, http.StatusBadRequest, errAnalystOff.Error())
		return
	}
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Question) == "" {
		writeError(w, http.StatusBadRequest, "ask a question")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	prompt := `You turn questions about application logs into a search for Visionary's Logs page.
Reply with one JSON object: {"query": "<search>", "since": "<15m|1h|6h|24h|7d or empty>", "note": "<one short sentence on what the search finds>"}.
` + logSearchHelp + `
Use only the services and fields listed when the question names one. Keep the search as simple as the question allows. "since" is the range the question asks about, empty when it names none.
Now is ` + time.Now().UTC().Format(time.RFC3339) + `.
` + s.logFieldsHint(ctx)
	messages := []chatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: body.Question}}
	for attempt := 0; attempt < 2; attempt++ {
		content, err := s.llm().chatJSON(ctx, messages)
		if err != nil {
			s.fail(w, err)
			return
		}
		var out struct {
			Query string `json:"query"`
			Since string `json:"since"`
			Note  string `json:"note"`
		}
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			writeError(w, http.StatusBadGateway, "the model did not return a search")
			return
		}
		if _, err := compileLogQuery(out.Query, s.q, s.onD1()); err != nil {
			messages = append(messages, chatMessage{Role: "assistant", Content: content},
				chatMessage{Role: "user", Content: "That search is invalid: " + err.Error() + ". Reply with a corrected JSON object."})
			continue
		}
		switch out.Since {
		case "15m", "1h", "6h", "24h", "7d":
		default:
			out.Since = ""
		}
		s.audit(r, "logs.ask", "", map[string]any{"question": clip(body.Question, 300), "query": out.Query})
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeError(w, http.StatusBadGateway, "the model could not write a valid search for that; try wording it differently")
}

func (s *Server) handleLogExplain(w http.ResponseWriter, r *http.Request) {
	if s.llm() == nil {
		writeError(w, http.StatusBadRequest, errAnalystOff.Error())
		return
	}
	var body struct {
		Q    string `json:"q"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	q := r.URL.Query()
	q.Set("from", body.From)
	q.Set("to", body.To)
	tr, err := parseRange(q, s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ls, err := s.logSearchFor(r, tr, body.Q)
	if err != nil {
		s.logFail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	pats, err := s.db.Query(ctx, `SELECT pattern, COUNT(*), MAX(severity), MIN(created_at), MAX(created_at), MAX(id) FROM app_logs WHERE `+ls.sql()+
		` GROUP BY pattern ORDER BY MAX(severity) DESC, COUNT(*) DESC LIMIT 25`, ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	var ids []string
	for _, row := range pats.Rows {
		if p := toString(row[0]); p != "" {
			ids = append(ids, p)
		}
	}
	templates := s.patternTemplates(ctx, ids)
	var b strings.Builder
	fmt.Fprintf(&b, "Search: %s\nRange: %s to %s\n\nMessage patterns (count, worst level, first, last, a line id):\n", quoteSearch(body.Q), tr.From.Format(time.RFC3339), tr.To.Format(time.RFC3339))
	for _, row := range pats.Rows {
		tpl := templates[toString(row[0])]
		if tpl == "" {
			tpl = "(not grouped yet)"
		}
		fmt.Fprintf(&b, "- %d× %s %q first %s last %s, e.g. line #%d\n", toInt(row[1]), levelNameOf(toInt(row[2])), tpl, toString(row[3]), toString(row[4]), toInt(row[5]))
	}
	lines, err := s.db.Query(ctx, `SELECT id, created_at, level, service, message, attrs, request_id FROM app_logs WHERE `+ls.sql()+
		` ORDER BY severity DESC, created_at DESC LIMIT 40`, ls.args...)
	if err == nil {
		res := &Result{Columns: lines.Columns, Rows: lines.Rows}
		s.maskLogRows(r, res)
		b.WriteString("\nSample lines, worst first:\n")
		for _, row := range res.Rows {
			fmt.Fprintf(&b, "#%d %s %s %s: %s %s", toInt(row[0]), toString(row[1]), toString(row[2]), toString(row[3]), truncate(toString(row[4]), 400), truncate(toString(row[5]), 300))
			if rid := toString(row[6]); rid != "" {
				b.WriteString(" request " + rid)
			}
			b.WriteString("\n")
		}
	}
	prompt := `You read application logs for developers and say what is going on.
Reply with one JSON object: {"summary": "<text>", "lines": [<ids of the lines you cite>]}.
The text: one or two sentences on the overall picture, then 2 to 5 short lines starting with "- ": the problems in order of importance, what each most likely means, and what to look at next. Cite lines as #id. Say plainly when nothing looks wrong. No headings.`
	content, err := s.llm().chatJSON(ctx, []chatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: b.String()}})
	if err != nil {
		s.fail(w, err)
		return
	}
	var out struct {
		Summary string  `json:"summary"`
		Lines   []int64 `json:"lines"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil || out.Summary == "" {
		writeError(w, http.StatusBadGateway, "the model did not return a summary")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
