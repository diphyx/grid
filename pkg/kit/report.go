package kit

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Result is one check.
type Result struct {
	Spec string `json:"spec"` // e.g. "§6"
	Name string `json:"name"`
	Pass bool   `json:"pass"`
	Skip bool   `json:"skip,omitempty"`
	Note string `json:"note,omitempty"`
}

// Report collects results and prints them as they come.
type Report struct {
	All []Result

	mutex sync.Mutex
	out   io.Writer
	json  bool
}

// NewReport writes results to out as they come; asJSON prints one JSON document at the end.
func NewReport(out io.Writer, asJSON bool) *Report { return &Report{out: out, json: asJSON} }

func (report *Report) add(result Result) {
	report.mutex.Lock()
	defer report.mutex.Unlock()

	report.All = append(report.All, result)
	if report.json {
		return
	}
	mark := "pass"
	switch {
	case result.Skip:
		{
			mark = "skip"
		}
	case !result.Pass:
		{
			mark = "FAIL"
		}
	}
	line := fmt.Sprintf("%s  %-4s %s", mark, result.Spec, result.Name)
	if result.Note != "" {
		line += "  — " + result.Note
	}
	fmt.Fprintln(report.out, line)
}

// Check records a pass if err is nil, else a fail with err.
func (report *Report) Check(spec, name string, problem error) bool {
	result := Result{Spec: spec, Name: name, Pass: problem == nil}
	if problem != nil {
		result.Note = problem.Error()
	}
	report.add(result)

	return problem == nil
}

// Skip records a check that did not run, and why.
func (report *Report) Skip(spec, name, why string) {
	report.add(Result{Spec: spec, Name: name, Pass: true, Skip: true, Note: why})
}

// Failures counts failed checks.
func (report *Report) Failures() int {
	report.mutex.Lock()
	defer report.mutex.Unlock()

	count := 0
	for _, result := range report.All {
		if !result.Pass {
			count++
		}
	}

	return count
}

// Finish prints the summary (or the JSON) and returns the failure count.
func (report *Report) Finish() int {
	count := report.Failures()
	if report.json {
		enc := json.NewEncoder(report.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Results  []Result `json:"results"`
			Failures int      `json:"failures"`
		}{report.All, count})

		return count
	}
	fmt.Fprintf(report.out, "\n%d checks, %d failed\n", len(report.All), count)

	return count
}
