package main

import (
	"encoding/xml"
	"fmt"
	"strings"
)

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Errors    int         `xml:"errors,attr"`
	Skipped   int         `xml:"skipped,attr"`
	Time      string      `xml:"time,attr"`
	Timestamp string      `xml:"timestamp,attr,omitempty"`
	Cases     []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Error     *junitMessage `xml:"error,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr,omitempty"`
	Type    string `xml:"type,attr,omitempty"`
	Text    string `xml:",chardata"`
}

func junitXML(b batch) []byte {
	name := fmt.Sprintf("Visionary uptime run #%d", b.ID)
	if len(b.Tags) > 0 {
		name += " (" + strings.Join(b.Tags, ", ") + ")"
	}
	s := junitSuite{Name: name, Timestamp: b.CreatedAt}
	total := 0.0
	for _, it := range b.Items {
		secs := 0.0
		if it.DurationMs != nil {
			secs = *it.DurationMs / 1000
		}
		total += secs
		c := junitCase{Name: it.CheckName, Classname: "visionary.uptime." + it.Kind, Time: fmt.Sprintf("%.3f", secs)}
		msg := it.Error
		if st := it.failedStep(); st != "" {
			msg = st + ": " + msg
		}
		detail := strings.TrimSpace(strings.Join([]string{msg, it.URL}, "\n"))
		switch it.Status {
		case "passed":
		case "failed":
			c.Failure = &junitMessage{Message: msg, Type: "CheckFailed", Text: detail}
			s.Failures++
		case "skipped":
			c.Skipped = &junitMessage{Message: msg}
			s.Skipped++
		default:
			if msg == "" {
				msg = "did not finish (" + it.Status + ")"
			}
			c.Error = &junitMessage{Message: msg, Type: "CheckError", Text: detail}
			s.Errors++
		}
		if it.URL != "" && c.Failure == nil && c.Error == nil {
			c.SystemOut = it.URL
		}
		s.Cases = append(s.Cases, c)
	}
	s.Tests = len(s.Cases)
	s.Time = fmt.Sprintf("%.3f", total)
	doc := junitSuites{Name: "visionary-checks", Tests: s.Tests, Failures: s.Failures, Errors: s.Errors, Skipped: s.Skipped, Time: s.Time, Suites: []junitSuite{s}}
	out, _ := xml.MarshalIndent(doc, "", "  ")
	return append([]byte(xml.Header), append(out, '\n')...)
}
