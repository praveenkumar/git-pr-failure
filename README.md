# git-pr-failure

A CLI tool that fetches failed CI job logs from GitHub pull requests, downloads artifacts, and analyzes them to explain why they failed. It queries both the Checks API and Commit Statuses API to find all failures — GitHub Actions, Prow, Jenkins, CircleCI, and other CI systems.

For GitHub Actions jobs, it downloads the actual test artifacts (JUnit XML, test logs, result files) rather than just console output. It then parses the downloaded files to provide a failure analysis summary.

## Installation

```bash
go install github.com/prkumar/git-pr-failure@latest
```

Or build from source:

```bash
git clone https://github.com/prkumar/git-pr-failure.git
cd git-pr-failure
make build
```

## Build

```bash
make build    # build a static binary
make clean    # remove built binary
make vendor   # go mod tidy + go mod vendor
```

## Usage

```
git-pr-failure <owner/repo> <pr-number> [--output <dir>]
```

### Arguments

| Argument | Description | Example |
|---|---|---|
| `<owner/repo>` | GitHub repository | `crc-org/crc` |
| `<pr-number>` | Pull request number | `5025` |
| `--output`, `-o` | Output directory (default: `/tmp/pr-<number>/`) | `--output ./logs` |

### Examples

```bash
# Basic usage
git-pr-failure crc-org/crc 5025

# Custom output directory
git-pr-failure --output ./my-logs kubernetes/kubernetes 12345
```

### Sample output

```
Failed CI Jobs for PR #5025 (head: 975a058)

NAME                                      CONCLUSION  CI SYSTEM       URL
Run OKD bundle with crc (1.25)            failure     GitHub Actions  https://github.com/crc-org/crc/actions/runs/...
ci/gh/e2e-microshift/windows-11-23h2-ent  failure     GitHub Actions  https://github.com/crc-org/crc/actions/runs/...
ci/prow/security                          failure     Prow            https://prow.ci.openshift.org/view/gs/...

Logs saved to /tmp/pr-5025/
  - Run-OKD-bundle-with-crc-1.25.log (140.2 KB)
  - windows-e2e-microshift-1123h2-ent/e2e.results (10.8 KB)
  - windows-e2e-microshift-1123h2-ent/crc-e2e-junit.xml (8.8 KB)
  - windows-e2e-microshift-1123h2-ent/e2e_2026-5-4_07-09-35.log (12.4 KB)

1 external CI job(s) — logs not available via GitHub API:
  - ci/prow/security (Prow): https://prow.ci.openshift.org/view/gs/...

Failure Analysis:

  [windows-e2e-microshift-1123h2-ent]
    Suite: Microshift test stories
    Test:  Start and expose a basic HTTP service and check after restart
    Step:  Step starting CRC with default bundle succeeds
    Error: Cannot determine if VM exists: ...macadam-windows-amd64.exe: The system cannot find the file specified.
    Skipped: 34 subsequent step(s)
```

## Features

- **Dual API coverage**: Queries both the Checks API (GitHub Actions, etc.) and Commit Statuses API (Prow, external reporters) for complete failure discovery
- **Artifact downloads**: For status-based GitHub Actions jobs, downloads actual workflow artifacts (JUnit XML, test logs, result files) instead of console output
- **ZIP extraction**: Automatically detects and extracts ZIP archives, preserving original filenames
- **RunID deduplication**: Multiple status entries sharing the same workflow run are downloaded only once
- **Failure analysis**: Parses downloaded files to explain why tests failed:
  - **JUnit XML**: Extracts test suite, test name, failed step, error message, and skipped step count
  - **Go test output**: Parses `--- FAIL:` blocks with `Error:` details from console logs
  - **GHA annotations**: Extracts `##[error]` lines (build errors, lint failures, etc.)
  - **Structured logs**: Falls back to `level=error` messages from log files

## Authentication

The tool needs a GitHub token. It checks these sources in order:

1. `GITHUB_TOKEN` environment variable
2. `GH_TOKEN` environment variable
3. `gh auth token` (GitHub CLI)

```bash
# Option 1: environment variable
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
git-pr-failure crc-org/crc 5025

# Option 2: if you have gh CLI authenticated, it just works
git-pr-failure crc-org/crc 5025
```

## How it works

1. Fetches the PR to get the head commit SHA
2. Queries the **Checks API** for check runs on that SHA (GitHub Actions, etc.)
3. Queries the **Commit Statuses API** for statuses on that SHA (Prow, external reporters)
4. Filters to failed jobs and prints a summary table
5. Downloads logs/artifacts for GitHub Actions failures:
   - Check run jobs: downloads console logs via `GetWorkflowJobLogs`
   - Status-based jobs: downloads workflow artifacts via `ListWorkflowRunArtifacts` + `DownloadArtifact`
6. Extracts ZIP archives and organizes artifacts into per-job subdirectories
7. Analyzes downloaded files (JUnit XML, Go test output, GHA annotations) and prints a failure summary
8. Lists external CI failures with URLs for manual inspection

## Design

See [docs/plan.md](docs/plan.md) for the full design document.
