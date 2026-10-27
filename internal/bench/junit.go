package bench

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// junitName names the test suites of junit.xml.
	junitName = "sidekick bench"
	// junitThresholds names the test suite of the thresholds.
	junitThresholds = "thresholds"
)

// junitSuites is the root element of junit.xml.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

// junitSuite is one scenario, or the thresholds.
type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

// junitCase is one run of a scenario, or one threshold.
type junitCase struct {
	ClassName string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure"`
	SystemOut string        `xml:"system-out,omitempty"`
}

// junitFailure says why a case failed.
type junitFailure struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// WriteJUnit writes the scored results as JUnit XML, for CI systems that
// show test results: one test suite per scenario with one test case per
// run, and a suite of the thresholds when there are any. A run fails when
// its verdict does not count as correct, or its root cause was graded and
// does not match.
func (r *Results) WriteJUnit(w io.Writer, thresholds []ThresholdResult) error {
	root := junitSuites{Name: junitName}

	for _, sc := range r.Scenarios {
		suite := junitSuite{Name: sc.ID}

		var took time.Duration

		for _, run := range sc.Runs {
			c := run.junitCase(sc)
			suite.Cases = append(suite.Cases, c)
			suite.Tests++
			took += run.Duration

			if c.Failure != nil {
				suite.Failures++
			}
		}

		suite.Time = seconds(took)
		root.Suites = append(root.Suites, suite)
		root.Tests += suite.Tests
		root.Failures += suite.Failures
	}

	if len(thresholds) > 0 {
		suite := thresholdSuite(thresholds)
		root.Suites = append(root.Suites, suite)
		root.Tests += suite.Tests
		root.Failures += suite.Failures
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write the JUnit header: %w", err)
	}

	enc := xml.NewEncoder(w)
	enc.Indent("", jsonIndent)

	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("write the JUnit XML: %w", err)
	}

	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write the JUnit XML: %w", err)
	}

	return nil
}

// junitCase returns the test case of one run of sc.
func (r RunResult) junitCase(sc ScenarioResult) junitCase {
	c := junitCase{
		ClassName: sc.ID,
		Name:      fmt.Sprintf("run %d", r.Run),
		Time:      seconds(r.Duration),
		SystemOut: fmt.Sprintf("verdict %s, expected %s; root cause %s; evidence %s; cost $%.4f",
			r.Report.Verdict, sc.Expected.Verdict, r.rootCauseCell(), shareCell(r.EvidenceScore), r.CostUSD()),
	}

	var problems []string

	if !r.VerdictCorrect {
		problems = append(problems, fmt.Sprintf("the verdict is %q; expected %s", r.Report.Verdict, sc.Expected.Verdict))
	}

	if rc := r.RootCause; rc != nil && !rc.Match {
		reason := fmt.Sprintf("the root cause does not match: score %.2f, every required item met: %t", rc.Score, rc.RequiredMet)
		if rc.Error != "" {
			reason = "the judge gave no grades: " + rc.Error
		}

		problems = append(problems, reason)
	}

	if len(problems) > 0 {
		c.Failure = &junitFailure{Message: problems[0], Text: strings.Join(problems, "\n")}
	}

	return c
}

// thresholdSuite returns the test suite of the thresholds, one test case
// each.
func thresholdSuite(thresholds []ThresholdResult) junitSuite {
	suite := junitSuite{Name: junitThresholds, Time: seconds(0)}

	for _, t := range thresholds {
		c := junitCase{
			ClassName: junitThresholds, Name: t.Name, Time: seconds(0),
			SystemOut: fmt.Sprintf("limit %.4g, value %s", t.Limit, valueCell(t.Value)),
		}

		if !t.Passed {
			c.Failure = &junitFailure{Message: fmt.Sprintf("%s is %s; the limit is %.4g", t.Name, valueCell(t.Value), t.Limit), Text: t.Detail}
			suite.Failures++
		}

		suite.Cases = append(suite.Cases, c)
		suite.Tests++
	}

	return suite
}

// seconds formats d as seconds with three decimals, as JUnit times are.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}
