package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-github/v85/github"
)

type FailedJob struct {
	Name       string
	Conclusion string
	CISystem   string
	URL        string
	JobID      int64
	RunID      int64
	HasLogs    bool
}

func getPRHeadSHA(ctx context.Context, client *github.Client, pr PRRef) (string, error) {
	pull, _, err := client.PullRequests.Get(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return "", fmt.Errorf("fetching PR #%d: %w", pr.Number, err)
	}
	return pull.GetHead().GetSHA(), nil
}

func findFailedJobs(ctx context.Context, client *github.Client, pr PRRef, headSHA string) ([]FailedJob, int, error) {
	var failed []FailedJob
	totalCount := 0

	// 1. Check Runs (Checks API)
	crOpts := &github.ListCheckRunsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		result, resp, err := client.Checks.ListCheckRunsForRef(ctx, pr.Owner, pr.Repo, headSHA, crOpts)
		if err != nil {
			return nil, 0, fmt.Errorf("listing check runs: %w", err)
		}
		totalCount += len(result.CheckRuns)
		for _, cr := range result.CheckRuns {
			conclusion := cr.GetConclusion()
			if conclusion != "failure" && conclusion != "timed_out" && conclusion != "cancelled" {
				continue
			}
			ci := classifyCI(cr)
			failed = append(failed, FailedJob{
				Name:       cr.GetName(),
				Conclusion: conclusion,
				CISystem:   ci,
				URL:        cr.GetHTMLURL(),
				JobID:      cr.GetID(),
				HasLogs:    ci == "GitHub Actions",
			})
		}
		if resp.NextPage == 0 {
			break
		}
		crOpts.Page = resp.NextPage
	}

	// 2. Commit Statuses (Status API) — covers Prow, external GH Actions reporters, etc.
	statusJobs, statusTotal, err := findFailedStatuses(ctx, client, pr, headSHA)
	if err != nil {
		return nil, 0, err
	}
	totalCount += statusTotal
	failed = append(failed, statusJobs...)

	return failed, totalCount, nil
}

func findFailedStatuses(ctx context.Context, client *github.Client, pr PRRef, headSHA string) ([]FailedJob, int, error) {
	// Collect all statuses, then deduplicate by context (keep the latest per context)
	type statusEntry struct {
		Context   string
		State     string
		TargetURL string
	}

	latest := make(map[string]statusEntry)
	opts := &github.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := client.Repositories.ListStatuses(ctx, pr.Owner, pr.Repo, headSHA, opts)
		if err != nil {
			return nil, 0, fmt.Errorf("listing commit statuses: %w", err)
		}
		for _, s := range statuses {
			sctx := s.GetContext()
			// API returns newest first, so first seen per context is the latest
			if _, exists := latest[sctx]; !exists {
				latest[sctx] = statusEntry{
					Context:   sctx,
					State:     s.GetState(),
					TargetURL: s.GetTargetURL(),
				}
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	var failed []FailedJob
	for _, s := range latest {
		if s.State != "failure" && s.State != "error" {
			continue
		}
		ci, hasLogs := classifyStatus(s.Context, s.TargetURL)
		job := FailedJob{
			Name:       s.Context,
			Conclusion: s.State,
			CISystem:   ci,
			URL:        s.TargetURL,
			HasLogs:    hasLogs,
		}
		if hasLogs {
			job.RunID = extractRunIDFromURL(s.TargetURL)
		}
		failed = append(failed, job)
	}

	return failed, len(latest), nil
}

func classifyStatus(context, targetURL string) (ciSystem string, hasGHALogs bool) {
	switch {
	case strings.HasPrefix(context, "ci/prow/"):
		return "Prow", false
	case strings.HasPrefix(context, "ci/gh/"):
		return "GitHub Actions", true
	case strings.Contains(context, "snyk"):
		return "Snyk", false
	case strings.Contains(context, "circleci"):
		return "CircleCI", false
	case strings.Contains(context, "jenkins"):
		return "Jenkins", false
	case strings.Contains(targetURL, "github.com") && strings.Contains(targetURL, "/actions/"):
		return "GitHub Actions", true
	default:
		return "external", false
	}
}

func extractRunIDFromURL(targetURL string) int64 {
	u, err := url.Parse(targetURL)
	if err != nil {
		return 0
	}
	// URL format: https://github.com/owner/repo/actions/runs/12345...
	parts := strings.Split(u.Path, "/")
	for i, p := range parts {
		if p == "runs" && i+1 < len(parts) {
			id, err := strconv.ParseInt(parts[i+1], 10, 64)
			if err == nil {
				return id
			}
		}
	}
	return 0
}

func classifyCI(cr *github.CheckRun) string {
	if cr.App == nil {
		return "unknown"
	}
	switch cr.App.GetSlug() {
	case "github-actions":
		return "GitHub Actions"
	case "jenkins", "jenkins-ci":
		return "Jenkins"
	case "circleci-checks":
		return "CircleCI"
	case "travis-ci":
		return "Travis CI"
	default:
		if name := cr.App.GetName(); name != "" {
			return name
		}
		return cr.App.GetSlug()
	}
}

func downloadAllLogs(ctx context.Context, client *github.Client, pr PRRef, jobs []FailedJob, outputDir string) ([]LogFile, []FailedJob, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating output directory: %w", err)
	}

	usedNames := make(map[string]int)
	seenRunIDs := make(map[int64]bool)
	var downloaded []LogFile
	var skipped []FailedJob

	for _, job := range jobs {
		if !job.HasLogs {
			skipped = append(skipped, job)
			continue
		}

		if job.RunID > 0 && job.JobID == 0 {
			if seenRunIDs[job.RunID] {
				continue
			}
			seenRunIDs[job.RunID] = true

			artifacts, err := downloadRunArtifacts(ctx, client, pr, job.RunID, outputDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to download artifacts for %q: %s\n", job.Name, err)
				skipped = append(skipped, job)
			} else {
				downloaded = append(downloaded, artifacts...)
			}
			continue
		}

		filename := uniqueFilename(usedNames, sanitizeFilename(job.Name)+".log")
		outPath := filepath.Join(outputDir, filename)

		size, err := downloadJobLog(ctx, client, pr, job, outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to download log for %q: %s\n", job.Name, err)
			skipped = append(skipped, job)
			continue
		}

		extracted, err := extractIfZip(outPath, outputDir, usedNames)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to extract zip for %q: %s\n", job.Name, err)
			downloaded = append(downloaded, LogFile{Name: filename, Size: size})
			continue
		}
		if extracted != nil {
			os.Remove(outPath)
			downloaded = append(downloaded, extracted...)
		} else {
			downloaded = append(downloaded, LogFile{Name: filename, Size: size})
		}
	}

	return downloaded, skipped, nil
}

func downloadJobLog(ctx context.Context, client *github.Client, pr PRRef, job FailedJob, outPath string) (int64, error) {
	if job.JobID > 0 {
		return downloadByJobID(ctx, client, pr, job.JobID, outPath)
	}
	return 0, fmt.Errorf("no job ID or run ID available")
}

func downloadByJobID(ctx context.Context, client *github.Client, pr PRRef, jobID int64, outPath string) (int64, error) {
	logURL, _, err := client.Actions.GetWorkflowJobLogs(ctx, pr.Owner, pr.Repo, jobID, 2)
	if err != nil {
		return 0, fmt.Errorf("getting log URL: %w", err)
	}
	return fetchAndSave(logURL.String(), outPath)
}

func downloadRunArtifacts(ctx context.Context, client *github.Client, pr PRRef, runID int64, outputDir string) ([]LogFile, error) {
	artifactList, _, err := client.Actions.ListWorkflowRunArtifacts(ctx, pr.Owner, pr.Repo, runID, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	if len(artifactList.Artifacts) == 0 {
		return nil, fmt.Errorf("no artifacts found for run %d", runID)
	}

	var logs []LogFile
	for _, artifact := range artifactList.Artifacts {
		artifactDir := filepath.Join(outputDir, sanitizeFilename(artifact.GetName()))
		if err := os.MkdirAll(artifactDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating artifact directory: %w", err)
		}

		dlURL, _, err := client.Actions.DownloadArtifact(ctx, pr.Owner, pr.Repo, artifact.GetID(), 2)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to get download URL for artifact %q: %s\n", artifact.GetName(), err)
			continue
		}

		tmpPath := filepath.Join(artifactDir, "artifact.zip")
		if _, err := fetchAndSave(dlURL.String(), tmpPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to download artifact %q: %s\n", artifact.GetName(), err)
			continue
		}

		usedNames := make(map[string]int)
		extracted, err := extractIfZip(tmpPath, artifactDir, usedNames)
		os.Remove(tmpPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to extract artifact %q: %s\n", artifact.GetName(), err)
			continue
		}

		prefix := sanitizeFilename(artifact.GetName())
		for _, lf := range extracted {
			lf.Name = filepath.Join(prefix, lf.Name)
			logs = append(logs, lf)
		}
	}
	return logs, nil
}

func fetchAndSave(logURL, outPath string) (int64, error) {
	resp, err := http.Get(logURL)
	if err != nil {
		return 0, fmt.Errorf("downloading log: %w", err)
	}
	defer resp.Body.Close()

	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("writing log: %w", err)
	}
	return n, nil
}

var zipMagic = []byte("PK\x03\x04")

func extractIfZip(path, outputDir string, usedNames map[string]int) ([]LogFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	header := make([]byte, 4)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, nil
	}
	if string(header) != string(zipMagic) {
		return nil, nil
	}

	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}
	defer r.Close()

	var logs []LogFile
	for _, zf := range r.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		baseName := filepath.Base(zf.Name)
		if filepath.Ext(baseName) == "" {
			baseName += ".log"
		}
		name := uniqueFilename(usedNames, sanitizeFilename(baseName))
		outPath := filepath.Join(outputDir, name)

		size, err := extractZipEntry(zf, outPath)
		if err != nil {
			return nil, fmt.Errorf("extracting %s: %w", zf.Name, err)
		}
		logs = append(logs, LogFile{Name: name, Size: size})
	}
	return logs, nil
}

func extractZipEntry(zf *zip.File, outPath string) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	out, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	return io.Copy(out, rc)
}
