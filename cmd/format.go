package cmd

import (
	"fmt"
	"time"
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

func dash(text string) string {
	if text == "" || text == "<nil>" {
		return "-"
	}
	return text
}
