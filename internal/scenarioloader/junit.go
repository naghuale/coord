package scenarioloader

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"

	"coord/internal/evaluation"
)

// JUnitTestSuites is the root JUnit XML element.
type JUnitTestSuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Suites   []JUnitTestSuite `xml:"testsuite"`
}

type JUnitTestSuite struct {
	XMLName   xml.Name        `xml:"testsuite"`
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Timestamp string          `xml:"timestamp,attr"`
	Cases     []JUnitTestCase `xml:"testcase"`
}

type JUnitTestCase struct {
	XMLName   xml.Name      `xml:"testcase"`
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *JUnitFailure `xml:"failure,omitempty"`
}

type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// WriteJUnit encodes evaluation results to JUnit XML.
func WriteJUnit(w io.Writer, suiteName string, results []evaluation.Result) error {
	return WriteJUnitWithScenarioIDs(w, suiteName, nil, results)
}

// WriteJUnitWithScenarioIDs encodes results using their corresponding scenario IDs.
// If scenarioIDs is nil, stable numbered testcase names are generated.
func WriteJUnitWithScenarioIDs(w io.Writer, suiteName string, scenarioIDs []string, results []evaluation.Result) error {
	if scenarioIDs != nil && len(scenarioIDs) != len(results) {
		return fmt.Errorf("scenario ID count %d does not match result count %d", len(scenarioIDs), len(results))
	}
	suite := JUnitTestSuite{
		Name:      suiteName,
		Tests:     len(results),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	for i, result := range results {
		name := fmt.Sprintf("scenario-%d", i+1)
		if scenarioIDs != nil {
			name = scenarioIDs[i]
		}
		testCase := JUnitTestCase{
			Name:      name,
			Classname: suiteName,
			Time:      "0.000",
		}
		if !result.Pass {
			suite.Failures++
			testCase.Failure = &JUnitFailure{
				Message: fmt.Sprintf("hard=%d", result.HardViolations),
				Body:    fmt.Sprintf("ScenarioID=%s HardViolations=%d", name, result.HardViolations),
			}
		}
		suite.Cases = append(suite.Cases, testCase)
	}
	suites := JUnitTestSuites{
		Name:     suiteName,
		Tests:    suite.Tests,
		Failures: suite.Failures,
		Suites:   []JUnitTestSuite{suite},
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(suites); err != nil {
		return err
	}
	return encoder.Flush()
}
