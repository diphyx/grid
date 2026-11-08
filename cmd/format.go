package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/diphyx/grid/pkg/history"
)

func ago(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	duration := time.Since(at)
	switch {
	case duration < time.Minute:
		{
			return fmt.Sprintf("%ds ago", int(duration.Seconds()))
		}
	case duration < time.Hour:
		{
			return fmt.Sprintf("%dm ago", int(duration.Minutes()))
		}
	case duration < 48*time.Hour:
		{
			return fmt.Sprintf("%dh%dm ago", int(duration.Hours()), int(duration.Minutes())%60)
		}
	}

	return at.Local().Format("2006-01-02 15:04")
}

// writeHistory prints history entries, one a line.
func writeHistory(writer io.Writer, entries []history.Entry) {
	fmt.Fprintln(writer, "AT\tKIND\tEVENT\tSUBJECT\tTOOK\tMESSAGE")
	for _, entry := range entries {
		took := "-"
		if entry.Took > 0 {
			took = entry.Took.Round(time.Millisecond).String()
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.At.Local().Format("2006-01-02 15:04:05"), entry.Kind, entry.Event, dash(entry.Subject), took, entry.Message)
	}
}

func dash(text string) string {
	if text == "" || text == "<nil>" {
		return "-"
	}
	return text
}
