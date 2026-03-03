---
name: gmail-triage
description: Run and maintain a local Gmail triage Go workflow, including OAuth credentials/token setup checks, safe dry runs, and optional live execution.
---

# Gmail Triage Skill

Use this skill when the user wants to run the Gmail triage workflow from the project repo root, especially in automation mode.

## Workdir and Required Files

- Workdir: `<repo-root>`
- Required files (in repo root):
  - `./credentials.json` (OAuth client credentials from Google Cloud)
  - `./token.json` (user OAuth token generated after first auth flow)

If either file is missing, do not attempt a live run. Provide the setup guide below.

## Credentials and Token Setup Guide

Provide this exact setup flow when the user asks how to create credentials/token files:

1. Open Google Cloud Console and select/create a project.
2. Enable `Gmail API` for that project.
3. Configure OAuth consent screen (External/Test mode is fine for personal setup).
4. Create OAuth client credentials with app type `Desktop app`.
5. Download the client JSON and save it as `<repo-root>/credentials.json`.
6. Run one interactive bootstrap command from `<repo-root>` to generate `token.json`:

```bash
cd <repo-root>
go run ./cmd/gmailtriage \
  --credentials ./credentials.json \
  --token ./token.json \
  --lookback_days 90 \
  --domain_action skip \
  --archive_old no
```

7. Open the printed auth URL and approve access.
8. Your browser may redirect to `http://localhost/...` and show an error page; this is expected for manual CLI OAuth.
9. Paste either the raw `code` query parameter value or the full redirected URL into terminal; the CLI extracts `code` automatically.
10. Verify token file permissions:

```bash
chmod 600 <repo-root>/token.json
```

11. If OAuth scopes change, delete `token.json` and re-run bootstrap to re-consent.

## Default Safe Run (Automation-Friendly)

Use this command for scheduled checks with no destructive actions:

```bash
cd <repo-root>
go test ./... && \
go run ./cmd/gmailtriage \
  --credentials ./credentials.json \
  --token ./token.json \
  --lookback_days 90 \
  --non_interactive \
  --domain_action skip \
  --archive_old no \
  --dry_run
```

## Live Run (Only after Explicit User Confirmation)

Run without `--dry_run` only when user confirms live changes.

```bash
cd <repo-root>
go test ./... && \
go run ./cmd/gmailtriage \
  --credentials ./credentials.json \
  --token ./token.json \
  --lookback_days 90 \
  --non_interactive \
  --domain_action skip \
  --archive_old no
```

## Scope Notes

- Required scopes are:
  - `gmail.readonly`
  - `gmail.modify`
  - `gmail.settings.basic`
- Gmail filter creation for future matching mail is always enabled.
- Consent text warning: Google may display broad wording such as `Read, compose, and send emails from your Gmail account` when requesting `gmail.modify`.
- This app uses `gmail.modify` for label operations only (add/remove labels and remove `INBOX`).
- There is no narrower scope for message-level label changes only. `gmail.labels` is for label resource management, not assigning/removing labels on messages.


- HTTP unsubscribe endpoints are attempted automatically in live runs.
- In `--dry_run`, unsubscribe HTTP calls are only previewed (not executed).
- Unsubscribe actions also archive inbox messages for the selected domain (remove `INBOX`).
- Interactive mode includes a granular sender-level unsubscribe+archive option within each domain.
- Domain/sender prompts show a latest-subject preview for decision context.
- Phase 3 queues selected actions first, then applies them with progress output.
- Phase 3 uses concurrent metadata workers with retry/backoff and a local cache for faster reruns.

## Expected Summary Output

After each run, summarize:

1. `go test` pass/fail
2. Count of political and calendar detections
3. Whether inbox labels were removed (political/calendar)
4. Domain histogram actions taken/skipped
5. Whether old-mail archive step was skipped/run
6. Any blockers (missing credentials/token, OAuth errors)
