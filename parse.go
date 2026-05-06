package main

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TestFailure struct {
	ArtifactDir string
	TestSuite   string
	TestName    string
	FailedStep  string
	ErrorMsg    string
	Skipped     int
}

type junitTestSuites struct {
	Suites []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name    string        `xml:"name,attr"`
	Status  string        `xml:"status,attr"`
	Failure *junitFailure `xml:"failure"`
	Errors  []junitError  `xml:"error"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitError struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
}

func analyzeFailures(outputDir string) []TestFailure {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
	}

	var failures []TestFailure

	for _, entry := range entries {
		if entry.IsDir() {
			dir := filepath.Join(outputDir, entry.Name())
			if f := parseJUnitDir(entry.Name(), dir); len(f) > 0 {
				failures = append(failures, f...)
				continue
			}
			if f := parseLogDir(entry.Name(), dir); len(f) > 0 {
				failures = append(failures, f...)
			}
			continue
		}

		path := filepath.Join(outputDir, entry.Name())
		if f := parseGHALog(entry.Name(), path); len(f) > 0 {
			failures = append(failures, f...)
		}
	}
	return failures
}

func parseJUnitDir(artifactName, dir string) []TestFailure {
	files, _ := filepath.Glob(filepath.Join(dir, "*.xml"))
	var failures []TestFailure
	for _, f := range files {
		failures = append(failures, parseJUnitFile(artifactName, f)...)
	}
	return failures
}

func parseJUnitFile(artifactName, path string) []TestFailure {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var suites junitTestSuites
	if err := xml.Unmarshal(data, &suites); err != nil {
		var single junitTestSuite
		if err := xml.Unmarshal(data, &single); err != nil {
			return nil
		}
		suites.Suites = []junitTestSuite{single}
	}

	var failures []TestFailure
	for _, suite := range suites.Suites {
		for _, tc := range suite.TestCases {
			if tc.Failure == nil {
				continue
			}

			skipped := 0
			for _, e := range tc.Errors {
				if e.Type == "skipped" {
					skipped++
				}
			}

			failedStep, errorMsg := extractStepAndError(tc.Failure)

			failures = append(failures, TestFailure{
				ArtifactDir: artifactName,
				TestSuite:   suite.Name,
				TestName:    tc.Name,
				FailedStep:  failedStep,
				ErrorMsg:    errorMsg,
				Skipped:     skipped,
			})
		}
	}
	return failures
}

func extractStepAndError(f *junitFailure) (step, errMsg string) {
	text := f.Body
	if text == "" {
		text = f.Message
	}

	if idx := strings.Index(text, "Step "); idx >= 0 {
		stepLine := text[idx:]
		if colonIdx := strings.Index(stepLine, ":"); colonIdx >= 0 {
			step = strings.TrimSpace(stepLine[:colonIdx])
		}
	}

	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "level=error") {
			if msgIdx := strings.Index(line, `msg="`); msgIdx >= 0 {
				msg := line[msgIdx+5:]
				if endIdx := strings.LastIndex(msg, `"`); endIdx >= 0 {
					msg = msg[:endIdx]
				}
				errMsg = msg
				break
			}
		}
	}

	if errMsg == "" && strings.Contains(text, "exit code:") {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "exit code:") {
				errMsg = line
				break
			}
		}
	}

	return step, errMsg
}

func parseLogDir(artifactName, dir string) []TestFailure {
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	var failures []TestFailure
	for _, f := range files {
		if errs := extractLogErrors(f); len(errs) > 0 {
			failures = append(failures, TestFailure{
				ArtifactDir: artifactName,
				TestName:    filepath.Base(f),
				ErrorMsg:    strings.Join(errs, "; "),
			})
		}
	}
	return failures
}

func parseGHALog(filename, path string) []TestFailure {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	baseName := strings.TrimSuffix(filename, filepath.Ext(filename))

	var ghaErrors []string
	ghaErrorSeen := make(map[string]bool)

	type pendingFail struct {
		name     string
		errorMsg string
	}
	var pending []pendingFail
	var goTestFailures []TestFailure
	var errorMsg string
	inError := false

	flushPending := func() {
		msg := strings.TrimSpace(errorMsg)
		for i := len(pending) - 1; i >= 0; i-- {
			if pending[i].errorMsg == "" && msg != "" {
				pending[i].errorMsg = msg
				msg = ""
			}
		}
		for _, p := range pending {
			if p.errorMsg != "" {
				goTestFailures = append(goTestFailures, TestFailure{
					ArtifactDir: baseName,
					TestName:    p.name,
					ErrorMsg:    p.errorMsg,
				})
			}
		}
		pending = nil
		errorMsg = ""
		inError = false
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		content := stripGHATimestamp(line)

		if idx := strings.Index(content, "##[error]"); idx >= 0 {
			msg := strings.TrimSpace(content[idx+9:])
			if msg != "" && !strings.HasPrefix(msg, "Process completed with exit code") {
				if !ghaErrorSeen[msg] {
					ghaErrorSeen[msg] = true
					ghaErrors = append(ghaErrors, msg)
				}
			}
			continue
		}

		trimmed := strings.TrimSpace(content)

		if strings.HasPrefix(trimmed, "--- FAIL:") {
			testName := strings.TrimPrefix(trimmed, "--- FAIL: ")
			if parenIdx := strings.LastIndex(testName, " ("); parenIdx >= 0 {
				testName = testName[:parenIdx]
			}
			pending = append(pending, pendingFail{name: testName, errorMsg: strings.TrimSpace(errorMsg)})
			errorMsg = ""
			inError = false
			continue
		}

		if (trimmed == "FAIL" || strings.HasPrefix(trimmed, "FAIL\t") ||
			strings.HasPrefix(trimmed, "=== RUN") || strings.HasPrefix(trimmed, "--- PASS:")) && len(pending) > 0 {
			flushPending()
			continue
		}

		if strings.HasPrefix(trimmed, "Error:") && !strings.HasPrefix(trimmed, "Error Trace:") {
			inError = true
			errorMsg = strings.TrimSpace(strings.TrimPrefix(trimmed, "Error:"))
			continue
		}

		if strings.HasPrefix(trimmed, "Error Trace:") || strings.HasPrefix(trimmed, "Test:") {
			inError = false
			continue
		}

		if inError && trimmed != "" {
			if errorMsg != "" {
				errorMsg += " "
			}
			errorMsg += trimmed
		}
	}
	flushPending()

	if len(goTestFailures) > 0 {
		return goTestFailures
	}

	if len(ghaErrors) > 0 {
		return []TestFailure{{
			ArtifactDir: baseName,
			TestName:    filename,
			ErrorMsg:    strings.Join(ghaErrors, "\n         "),
		}}
	}
	return nil
}

func stripGHATimestamp(line string) string {
	if len(line) > 20 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		if zIdx := strings.IndexByte(line[19:], 'Z'); zIdx >= 0 && zIdx < 15 {
			return line[19+zIdx+1:]
		}
	}
	return line
}

func extractLogErrors(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := make(map[string]bool)
	var errors []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "level=error") {
			continue
		}
		if msgIdx := strings.Index(line, `msg="`); msgIdx >= 0 {
			msg := line[msgIdx+5:]
			if endIdx := strings.LastIndex(msg, `"`); endIdx >= 0 {
				msg = msg[:endIdx]
			}
			if !seen[msg] {
				seen[msg] = true
				errors = append(errors, msg)
			}
		}
	}
	return errors
}

func printFailureAnalysis(failures []TestFailure) {
	if len(failures) == 0 {
		return
	}

	fmt.Printf("\nFailure Analysis:\n")
	for _, f := range failures {
		fmt.Printf("\n  [%s]\n", f.ArtifactDir)
		if f.TestSuite != "" {
			fmt.Printf("    Suite: %s\n", f.TestSuite)
		}
		fmt.Printf("    Test:  %s\n", f.TestName)
		if f.FailedStep != "" {
			fmt.Printf("    Step:  %s\n", f.FailedStep)
		}
		if f.ErrorMsg != "" {
			fmt.Printf("    Error: %s\n", f.ErrorMsg)
		}
		if f.Skipped > 0 {
			fmt.Printf("    Skipped: %d subsequent step(s)\n", f.Skipped)
		}
	}
}
