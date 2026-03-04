# Gmail Inbox Triage Automation (Go)

This project provides an interactive Gmail triage tool that matches your workflow:

1. OAuth token with limited Gmail scopes for read + label modification.
2. Auto-label obvious categories:
   - Political mail (detect common FEC disclosure phrases) and archive by removing `INBOX`.
   - Google Calendar reminders:
     - Existing matching unread inbox mail is labeled and archived by removing `INBOX`.
     - Future matching mail is labeled by filter but keeps `INBOX` (not auto-archived).
3. For remaining unread inbox-only mail in the `--lookback_days` window:
   - Build a sender-domain histogram.
   - For each domain, choose to:
     - Label + archive existing mail and create a future Gmail filter.
     - Attempt domain-wide unsubscribe via `List-Unsubscribe` headers and archive inbox mail for that domain.
     - Choose granular sender-by-sender unsubscribe + archive within a domain.
     - Leave as-is.
   - During review, show the latest subject as context for each domain/sender prompt.
   - Queue selected actions first, then apply them with a phase-3 execution progress indicator.
   - Quit early at any point for long-tail domains.
4. Optionally archive inbox mail older than `--lookback_days`.

## Setup

1. Create a Google Cloud project and enable Gmail API.
2. Create OAuth Client credentials for a Desktop app.
3. Save credentials as `credentials.json` in this directory.
4. Run:

```bash
go run ./cmd/gmailtriage \
  --credentials ./credentials.json \
  --token ./token.json \
  --lookback_days 90
```

Optional flags:

- `--dry_run` preview only, no Gmail changes.
- `--domain_limit N` only walk top `N` domains in interactive triage.
- `--non_interactive` run with no prompts (automation-safe).
- `--domain_action ask|label|unsubscribe|skip` default action for each domain in non-interactive mode.
- `--archive_old ask|yes|no` old-mail archive behavior in non-interactive mode.
- `--scan_workers N` number of concurrent metadata workers for unread triage scan (1-25, default 12).
- `--metadata_cache PATH` local metadata cache file used to skip unchanged message fetches across reruns.

## OAuth Token Behavior

- The first run opens a browser auth URL and asks for an auth code.
- After approval, the browser may redirect to `http://localhost/...` and show an error page; this is expected for manual CLI OAuth.
- Paste either the raw `code` value or the full redirected URL into the terminal prompt; the CLI extracts `code` automatically.
- Token is stored at `token.json` (or `--token` path) with `0600` permissions.
- If you change scopes, delete the token file and re-run to re-consent.

## Notes About Scope and Safety

- Required OAuth scopes are:
  - `https://www.googleapis.com/auth/gmail.readonly`
  - `https://www.googleapis.com/auth/gmail.modify`
  - `https://www.googleapis.com/auth/gmail.settings.basic`
- Filter creation is always enabled when labels are created.
- Political detection intentionally does **not** use OCR or attachment parsing.
- Unsubscribe handling uses `List-Unsubscribe` headers:
  - HTTP unsubscribe endpoints are attempted automatically in live runs.
  - In `--dry_run`, HTTP unsubscribe attempts are reported but not executed.
  - `mailto:` unsubscribe links are always reported for manual action.
  - Unsubscribe actions archive inbox messages for the selected domain (`INBOX` removed).
- Interactive mode includes a granular sender-level unsubscribe+archive option within each domain.

### Scope-to-Feature Mapping

- `gmail.readonly`:
  - List messages and read metadata/snippets for classification and histogram generation.
- `gmail.modify`:
  - Add/remove message labels, including removing `INBOX` for archive behavior.
  - Required for `users.messages.modify` and `users.messages.batchModify` operations.
- `gmail.settings.basic`:
  - Create Gmail filters for future mail:
    - Political and domain filters auto-label + remove `INBOX`.
    - Calendar filters auto-label only (do not remove `INBOX`).

### Why Consent Text Looks Broad

Google may show a consent message like:

- `Read, compose, and send emails from your Gmail account`

when requesting `gmail.modify`. This is expected for that scope.

This app uses `gmail.modify` only for message label changes and inbox removal.

### Is There a Narrower "Labels-Only on Messages" Scope?

No. There is no narrower Gmail scope that allows only assigning/removing labels on existing messages.

- `gmail.labels` is narrower, but it is for label resource management (create/update/delete labels), not message-level label modification.
- Message-level label changes require `gmail.modify`.
