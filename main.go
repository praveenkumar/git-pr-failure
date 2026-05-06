package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	outputDir := flag.String("output", "", "output directory for logs (default: /tmp/pr-<number>/)")
	flag.StringVar(outputDir, "o", "", "output directory for logs (shorthand)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: git-pr-failure <owner/repo> <pr-number> [--output <dir>]\n\n")
		fmt.Fprintf(os.Stderr, "Fetch failed CI job logs for a GitHub pull request.\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  <owner/repo>   GitHub repository (e.g., kubernetes/kubernetes)\n")
		fmt.Fprintf(os.Stderr, "  <pr-number>    Pull request number\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}

	pr, err := parsePR(args[0], args[1])
	if err != nil {
		return err
	}

	if *outputDir == "" {
		*outputDir = fmt.Sprintf("/tmp/pr-%d", pr.Number)
	}

	ctx := context.Background()

	token, err := resolveToken()
	if err != nil {
		return err
	}

	client := newGitHubClient(ctx, token)

	headSHA, err := getPRHeadSHA(ctx, client, pr)
	if err != nil {
		return err
	}

	failed, total, err := findFailedJobs(ctx, client, pr, headSHA)
	if err != nil {
		return err
	}

	if total == 0 {
		fmt.Println("No check runs found for this PR.")
		return nil
	}

	if len(failed) == 0 {
		fmt.Printf("All %d jobs passed!\n", total)
		return nil
	}

	printSummary(failed, pr.Number, headSHA)

	downloaded, skipped, err := downloadAllLogs(ctx, client, pr, failed, *outputDir)
	if err != nil {
		return err
	}

	printResult(*outputDir, downloaded, skipped)

	failures := analyzeFailures(*outputDir)
	printFailureAnalysis(failures)

	return nil
}
