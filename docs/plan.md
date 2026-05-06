# Plan: `git-pr-failure` CLI Tool

## Context

A Go CLI tool that takes a GitHub PR reference, discovers all failed CI jobs (GitHub Actions and external CI), downloads available logs and artifacts, analyzes them for failure reasons, and saves them to disk for review.

The tool queries two GitHub APIs to get complete coverage of CI failures:
- **Checks API** — covers GitHub Actions and CI systems that report as check runs
- **Commit Statuses API** — covers Prow, external GitHub Actions reporters, and legacy CI integrations

## CLI Usage

```
git-pr-failure <owner/repo> <pr-number> [--output <dir>]
```

- **`<owner/repo>`** — required first argument (e.g., `kubernetes/kubernetes`)
- **`<pr-number>`** — required second argument, the PR number (e.g., `123`)
- **`--output` / `-o`** — optional flag to override the default output directory (`/tmp/pr-<number>/`)

## Build

```bash
make build    # static binary with CGO_ENABLED=0
make clean    # remove built binary
make vendor   # go mod tidy + go mod vendor
```

## Project Structure

```
go.mod
go.sum
Makefile     -- build (static binary), clean, vendor targets
main.go      -- entry point, arg parsing, orchestration (run function)
auth.go      -- GitHub token resolution (GITHUB_TOKEN, GH_TOKEN, gh auth token)
pr.go        -- PR argument parsing
ci.go        -- check run + commit status discovery, CI classification, log/artifact downloading
output.go    -- summary table printing, directory setup, filename sanitization
parse.go     -- failure analysis: JUnit XML parsing, Go test output parsing, GHA log parsing
```

Single `main` package. No CLI framework — just `os.Args` for positional args + `flag` for `--output`.

## Dependencies

- `github.com/google/go-github/v85` — GitHub API client
- `golang.org/x/oauth2` — token-based auth for the HTTP client

## Flow

1. Parse CLI args: `<owner/repo>` (split on `/`) + `<pr-number>` -> `PRRef{Owner, Repo, Number}`
2. Resolve GitHub token (env vars -> `gh auth token` fallback)
3. Build authenticated `*github.Client`
4. Fetch PR -> extract head SHA
5. List all check runs for head SHA (Checks API, paginated)
6. List all commit statuses for head SHA (Status API, paginated, deduplicated by context)
7. Filter both to failed (conclusion/state: `failure`, `timed_out`, `cancelled`, `error`)
8. Classify each job's CI system (GitHub Actions, Prow, Jenkins, CircleCI, etc.)
9. Print summary table (name, conclusion, CI system, URL)
10. For GitHub Actions failures:
    - Check run-based (JobID): download job logs via `Actions.GetWorkflowJobLogs`
    - Status-based (RunID): download workflow artifacts via `Actions.ListWorkflowRunArtifacts` + `Actions.DownloadArtifact`
    - Deduplicate status-based downloads by RunID (multiple statuses may share the same run)
11. Handle ZIP files: detect via magic bytes (`PK\x03\x04`), extract contents preserving original filenames
12. Print output directory and per-file summary
13. List external CI jobs whose logs can't be fetched via GitHub API
14. Analyze downloaded logs/artifacts for failure reasons and print failure analysis

## Key Implementation Details

**Authentication** (`auth.go`):
- Check `GITHUB_TOKEN` env -> `GH_TOKEN` env -> exec `gh auth token`
- Clear error message if all three fail

**Arg Parsing** (`pr.go`):
- `<owner/repo>` arg: split on `/` to get owner and repo name; validate exactly one `/`
- `<pr-number>` arg: parse with `strconv.Atoi`; validate it's a positive integer

**Job Discovery** (`ci.go`):
- **Check Runs**: `client.Checks.ListCheckRunsForRef` — paginate with PerPage=100. For GitHub Actions, the check run ID equals the workflow job ID.
- **Commit Statuses**: `client.Repositories.ListStatuses` — paginate, deduplicate by context (keep newest). Classify by context prefix (`ci/prow/`, `ci/gh/`, etc.) and target URL.
- Status-based GitHub Actions jobs use the workflow run ID extracted from the target URL.

**Log & Artifact Download** (`ci.go`):
- Check run jobs (JobID > 0): `GetWorkflowJobLogs` — returns plain text console output
- Status-based jobs (RunID > 0): `ListWorkflowRunArtifacts` + `DownloadArtifact` — downloads actual test artifacts (JUnit XML, test logs, results files) into per-artifact subdirectories
- RunID deduplication: multiple status-based jobs sharing the same RunID are downloaded only once
- ZIP handling: downloaded files are checked for ZIP magic bytes; ZIPs are extracted and the archive removed
- Filename extensions are preserved from the original files (no forced `.log` suffix)
- Download failures are non-fatal — warn and continue with remaining jobs

**Failure Analysis** (`parse.go`):
- **JUnit XML**: parses `<testsuites>` / `<testsuite>` / `<testcase>` / `<failure>` elements. Extracts test suite, test name, failed step, error message (`level=error` lines), and count of skipped subsequent steps.
- **Go test output**: parses `--- FAIL:` lines and associated `Error:` blocks from GHA console logs. Handles both parent-first ordering (subtests) and error-before-FAIL ordering. Collects multi-line error messages.
- **GHA error annotations**: parses `##[error]` lines from GitHub Actions console logs, filtering out generic "Process completed with exit code" messages.
- **`level=error` log lines**: fallback parser for artifact log files containing structured log output.
- Priority: JUnit XML > `level=error` logs (for artifact dirs); Go test failures > GHA annotations (for console logs).

**Output** (`output.go`):
- Summary table via `text/tabwriter`
- Filename sanitization: replace `/`, `\`, `:`, spaces with `-`; collapse multiple dashes
- Handle filename collisions with `-2`, `-3` counter suffix (extension-aware)
- Create output dir with `os.MkdirAll`; overwrite existing files

## Edge Cases

- No check runs or statuses found -> print message, exit 0
- All jobs passed -> print "All N jobs passed!", exit 0
- External CI failures (Prow, Jenkins, etc.) -> shown in table with URLs, logs not downloadable
- Log download failure for individual job -> warn, continue with others
- Output directory already exists -> overwrite files in place
- Duplicate sanitized filenames -> append `-2`, `-3` suffix
- Commit statuses have duplicates per context -> only latest (first returned by API) is used
- Multiple statuses sharing same RunID -> artifacts downloaded only once
- Downloaded file is a ZIP -> automatically extracted, archive removed
- No artifacts found for a workflow run -> warn, skip
- GHA timestamp format varies (different nanosecond precision) -> robust timestamp stripping
