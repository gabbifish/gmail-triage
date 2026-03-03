package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gmailtriage/internal/gmailclient"
	"gmailtriage/internal/phases"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type app struct {
	gmailClient       *gmailclient.Client
	httpClient        *http.Client
	reader            *bufio.Reader
	dryRun            bool
	lookbackDays      int
	nonInteractive    bool
	domainAction      string
	archiveOldPolicy  string
	scanWorkers       int
	metadataCachePath string
}

const (
	phaseKnownDefaultRules = 1
	phaseDomainTriage      = 2
	phaseInboxCleanup      = 3
)

func main() {
	var (
		credentialsPath = flag.String("credentials", "credentials.json", "Path to OAuth client credentials JSON.")
		tokenPath       = flag.String("token", "token.json", "Path to store OAuth token.")
		lookbackDays    = flag.Int("lookback_days", 90, "How far back to inspect unread inbox messages.")
		dryRun          = flag.Bool("dry_run", false, "Preview changes without modifying Gmail data.")
		domainLimit     = flag.Int("domain_limit", 0, "Optional max number of domains to walk interactively. 0 means all.")
		nonInteractive  = flag.Bool("non_interactive", false, "Run without prompts.")
		domainAction    = flag.String("domain_action", "ask", "Action for each domain: ask|label|unsubscribe|skip.")
		archiveOld      = flag.String("archive_old", "ask", "Older-than-3-month inbox archive behavior: ask|yes|no.")
		startPhase      = flag.Int("start_phase", phaseKnownDefaultRules, "Phase to start from: 1, 2, or 3.")
		scanWorkers     = flag.Int("scan_workers", 12, "Number of concurrent workers (1-25) for unread triage metadata fetch.")
		metadataCache   = flag.String("metadata_cache", ".gmailtriage_metadata_cache.json", "Path to local metadata cache file for unread triage.")
	)
	flag.Parse()

	if *lookbackDays <= 0 {
		exitf("lookback_days must be positive")
	}
	if !isValidChoice(*domainAction, []string{"ask", "label", "unsubscribe", "skip"}) {
		exitf("domain_action must be one of: ask, label, unsubscribe, skip")
	}
	if !isValidChoice(*archiveOld, []string{"ask", "yes", "no"}) {
		exitf("archive_old must be one of: ask, yes, no")
	}
	if *startPhase < phaseKnownDefaultRules || *startPhase > phaseInboxCleanup {
		exitf("start_phase must be one of: 1, 2, 3")
	}
	if *scanWorkers < 1 || *scanWorkers > 25 {
		exitf("scan_workers must be in range 1-25")
	}

	ctx := context.Background()
	scopes := []string{gmail.GmailReadonlyScope, gmail.GmailModifyScope, gmail.GmailSettingsBasicScope}

	client, err := oauthClient(ctx, *credentialsPath, *tokenPath, scopes)
	if err != nil {
		exitf("oauth setup failed: %v", err)
	}

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		exitf("failed to create Gmail service: %v", err)
	}

	a := &app{
		gmailClient:       gmailclient.New(svc, gmailclient.Options{DryRun: *dryRun}),
		httpClient:        &http.Client{Timeout: 15 * time.Second},
		reader:            bufio.NewReader(os.Stdin),
		dryRun:            *dryRun,
		lookbackDays:      *lookbackDays,
		nonInteractive:    *nonInteractive,
		domainAction:      *domainAction,
		archiveOldPolicy:  *archiveOld,
		scanWorkers:       *scanWorkers,
		metadataCachePath: *metadataCache,
	}

	if *startPhase <= phaseKnownDefaultRules {
		fmt.Println("Phase 1: Applying known default rules...")
		if err := a.autoLabelCategories(ctx); err != nil {
			exitf("phase 1 failed: %v", err)
		}
	}

	if *startPhase <= phaseDomainTriage {
		fmt.Println("\nPhase 2: Triage most frequently unread inbox mail...")
		if err := a.domainTriage(ctx, *domainLimit); err != nil {
			exitf("phase 2 failed: %v", err)
		}
	}

	if *startPhase <= phaseInboxCleanup {
		fmt.Println("\nPhase 3: Inbox bankruptcy cleanup (archive older inbox mail)...")
		if err := a.optionalOldMailArchive(ctx); err != nil {
			exitf("phase 3 failed: %v", err)
		}
	}

	fmt.Println("\nDone.")
}

func oauthClient(ctx context.Context, credentialsPath, tokenPath string, scopes []string) (*http.Client, error) {
	credentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	config, err := google.ConfigFromJSON(credentials, scopes...)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	token, err := loadToken(tokenPath)
	if err != nil {
		token, err = requestTokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenPath, token); err != nil {
			return nil, err
		}
	}

	return config.Client(ctx, token), nil
}

func requestTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf(`Open this URL, approve access, and continue OAuth:
%v

After approval, your browser may land on http://localhost/... and show a site error.
That is expected for this CLI flow.
Paste either:
- the raw auth code value, or
- the full redirected URL (http://localhost/?...&code=...)

Code or URL: `, authURL)

	reader := bufio.NewReader(os.Stdin)
	codeInput, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read auth code: %w", err)
	}
	code := extractAuthCodeInput(codeInput)
	if code == "" {
		return nil, fmt.Errorf("no auth code detected in input")
	}

	token, err := config.Exchange(context.Background(), code)
	if err != nil {
		return nil, fmt.Errorf("exchange auth code: %w", err)
	}
	return token, nil
}

func extractAuthCodeInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	lowerInput := strings.ToLower(input)
	if strings.HasPrefix(lowerInput, "http://") || strings.HasPrefix(lowerInput, "https://") {
		if u, err := url.Parse(input); err == nil {
			if code := strings.TrimSpace(u.Query().Get("code")); code != "" {
				return code
			}
		}
	}

	queryInput := strings.TrimPrefix(input, "?")
	if values, err := url.ParseQuery(queryInput); err == nil {
		if code := strings.TrimSpace(values.Get("code")); code != "" {
			return code
		}
	}

	return input
}

func loadToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	token := &oauth2.Token{}
	if err := json.NewDecoder(f).Decode(token); err != nil {
		return nil, err
	}
	return token, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create token dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open token file: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(token); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

func (a *app) promptChoice(prompt, defaultChoice string, valid map[string]struct{}) string {
	for {
		fmt.Printf("%s ", prompt)
		input, err := a.reader.ReadString('\n')
		if err != nil {
			return defaultChoice
		}
		choice := strings.ToLower(strings.TrimSpace(input))
		if choice == "" {
			return defaultChoice
		}
		if _, ok := valid[choice]; ok {
			return choice
		}
		fmt.Println("Invalid choice.")
	}
}

func (a *app) autoLabelCategories(ctx context.Context) error {
	runner := &phases.Phase2Runner{
		LookbackDays: a.lookbackDays,
		Client:       a.gmailClient,
	}
	return runner.Run(ctx)
}

func (a *app) domainTriage(ctx context.Context, domainLimit int) error {
	runner := &phases.Phase3Runner{
		LookbackDays:      a.lookbackDays,
		DryRun:            a.dryRun,
		NonInteractive:    a.nonInteractive,
		DomainAction:      a.domainAction,
		Phase3ScanWorkers: a.scanWorkers,
		Phase3CachePath:   a.metadataCachePath,
		HTTPClient:        a.httpClient,
		Client:            a.gmailClient,
		PromptChoice:      a.promptChoice,
	}
	return runner.Run(ctx, domainLimit)
}

func (a *app) optionalOldMailArchive(ctx context.Context) error {
	runner := &phases.Phase4Runner{
		ArchiveOldPolicy: a.archiveOldPolicy,
		NonInteractive:   a.nonInteractive,
		Client:           a.gmailClient,
		PromptChoice:     a.promptChoice,
	}
	return runner.Run(ctx)
}

func isValidChoice(choice string, valid []string) bool {
	for _, v := range valid {
		if choice == v {
			return true
		}
	}
	return false
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
