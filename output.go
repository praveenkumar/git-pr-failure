package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
)

type LogFile struct {
	Name string
	Size int64
}

func printSummary(jobs []FailedJob, prNumber int, headSHA string) {
	short := headSHA
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Printf("\nFailed CI Jobs for PR #%d (head: %s)\n\n", prNumber, short)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCONCLUSION\tCI SYSTEM\tURL")
	for _, j := range jobs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", j.Name, j.Conclusion, j.CISystem, j.URL)
	}
	w.Flush()
	fmt.Println()
}

func printResult(outputDir string, downloaded []LogFile, skipped []FailedJob) {
	if len(downloaded) > 0 {
		fmt.Printf("Logs saved to %s/\n", outputDir)
		for _, f := range downloaded {
			fmt.Printf("  - %s (%s)\n", f.Name, formatSize(f.Size))
		}
	}

	var external []FailedJob
	for _, j := range skipped {
		if j.HasLogs {
			continue
		}
		external = append(external, j)
	}
	if len(external) > 0 {
		fmt.Printf("\n%d external CI job(s) — logs not available via GitHub API:\n", len(external))
		for _, j := range external {
			fmt.Printf("  - %s (%s): %s\n", j.Name, j.CISystem, j.URL)
		}
	}
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

var multiDash = regexp.MustCompile(`-{2,}`)

func sanitizeFilename(name string) string {
	r := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", " ", "-",
		"(", "", ")", "",
	)
	name = r.Replace(name)
	name = multiDash.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	return name
}

func uniqueFilename(used map[string]int, name string) string {
	used[name]++
	if used[name] == 1 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s-%d%s", base, used[name], ext)
}
