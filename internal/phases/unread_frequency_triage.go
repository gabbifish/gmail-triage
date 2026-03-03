package phases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gmailtriage/internal/gmailapiutil"
	"gmailtriage/internal/gmailclient"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

const phase3CacheVersion = 1

type Phase3Runner struct {
	LookbackDays      int
	DryRun            bool
	NonInteractive    bool
	DomainAction      string
	Phase3ScanWorkers int
	Phase3CachePath   string
	HTTPClient        *http.Client

	Client       *gmailclient.Client
	PromptChoice func(prompt, defaultChoice string, valid map[string]struct{}) string
}

type domainBucket struct {
	Domain              string
	Count               int
	MessageIDs          []string
	Senders             map[string]int
	SenderSample        map[string]string
	LatestSubject       string
	SenderLatestSubject map[string]string
}

type senderCount struct {
	Address string
	Count   int
}

type phase3Action struct {
	Description string
	Run         func(context.Context) error
}

type phase3CachedMessage struct {
	From     string   `json:"from"`
	Subject  string   `json:"subject"`
	LabelIDs []string `json:"label_ids"`
	Address  string   `json:"address"`
	Domain   string   `json:"domain"`
}

type phase3MetadataCache struct {
	Version       int                            `json:"version"`
	LastHistoryID uint64                         `json:"last_history_id,omitempty"`
	Messages      map[string]phase3CachedMessage `json:"messages"`
}

type phase3FetchResult struct {
	ID    string
	Entry phase3CachedMessage
	Err   error
}

// Run executes phase 2 unread-domain triage for messages in the lookback window.
// It scans unread inbox-only mail, builds domain buckets, collects selected user actions,
// then applies label/filter/unsubscribe/archive operations according to those selections.
func (r *Phase3Runner) Run(ctx context.Context, domainLimit int) error {
	if err := r.validate(); err != nil {
		return err
	}

	query := fmt.Sprintf("in:inbox is:unread newer_than:%dd", r.LookbackDays)
	ids, err := r.Client.ListMessageIDs(ctx, query, []string{"INBOX", "UNREAD"}, 0)
	if err != nil {
		return err
	}

	cache, err := loadPhase3MetadataCache(r.Phase3CachePath)
	if err != nil {
		fmt.Printf("Phase 3 cache load failed (%v). Continuing without cache.\n", err)
		cache = &phase3MetadataCache{Version: phase3CacheVersion, Messages: map[string]phase3CachedMessage{}}
	}

	currentHistoryID, err := r.currentMailboxHistoryID(ctx)
	if err != nil {
		fmt.Printf("Phase 3 cache history lookup failed (%v). Using full metadata refresh this run.\n", err)
		currentHistoryID = 0
	}

	changedIDs := map[string]struct{}{}
	historyUsable := false
	if cache.LastHistoryID > 0 {
		changedIDs, historyUsable, err = r.changedMessageIDsSince(ctx, cache.LastHistoryID)
		if err != nil {
			fmt.Printf("Phase 3 cache diff failed (%v). Using full metadata refresh this run.\n", err)
			historyUsable = false
			changedIDs = map[string]struct{}{}
		} else if historyUsable {
			fmt.Printf("Phase 3 cache: %d changed messages since last run.\n", len(changedIDs))
		} else {
			fmt.Println("Phase 3 cache history window expired; using full metadata refresh this run.")
		}
	}

	entriesByID := make(map[string]phase3CachedMessage, len(ids))
	toRefresh := make([]string, 0)
	for _, id := range ids {
		if historyUsable {
			if entry, ok := cache.Messages[id]; ok {
				if _, changed := changedIDs[id]; !changed {
					entriesByID[id] = entry
					continue
				}
			}
		}
		toRefresh = append(toRefresh, id)
	}

	freshEntries, err := r.fetchPhase3MetadataEntries(ctx, toRefresh)
	if err != nil {
		return err
	}
	for id, entry := range freshEntries {
		entriesByID[id] = entry
		cache.Messages[id] = entry
	}
	if currentHistoryID > 0 {
		cache.LastHistoryID = currentHistoryID
	}
	if err := savePhase3MetadataCache(r.Phase3CachePath, cache); err != nil {
		fmt.Printf("Phase 3 cache save failed (%v).\n", err)
	}

	buckets := map[string]*domainBucket{}
	if len(ids) > 0 {
		reportScanProgress("Phase 3 scan", 0, len(ids))
	}
	for i, id := range ids {
		entry, ok := entriesByID[id]
		if !ok {
			return fmt.Errorf("missing phase 3 metadata for message %s", id)
		}
		reportScanProgress("Phase 3 scan", i+1, len(ids))

		if !isInboxOnly(entry.LabelIDs) {
			continue
		}
		if entry.Address == "" || entry.Domain == "" {
			continue
		}
		subject := summarizeSubject(entry.Subject)

		bucket, ok := buckets[entry.Domain]
		if !ok {
			bucket = &domainBucket{
				Domain:              entry.Domain,
				Senders:             map[string]int{},
				SenderSample:        map[string]string{},
				SenderLatestSubject: map[string]string{},
			}
			buckets[entry.Domain] = bucket
		}
		bucket.Count++
		bucket.MessageIDs = append(bucket.MessageIDs, id)
		bucket.Senders[entry.Address]++
		if bucket.LatestSubject == "" {
			bucket.LatestSubject = subject
		}
		if _, exists := bucket.SenderSample[entry.Address]; !exists {
			bucket.SenderSample[entry.Address] = id
			bucket.SenderLatestSubject[entry.Address] = subject
		}
	}

	ordered := sortBucketsByCount(buckets)
	if len(ordered) == 0 {
		fmt.Println("No unread inbox-only messages found in the lookback window.")
		return nil
	}

	maxDomains := len(ordered)
	if domainLimit > 0 && domainLimit < maxDomains {
		maxDomains = domainLimit
	}

	fmt.Println("Top sender domains:")
	for idx, bucket := range ordered {
		if idx >= maxDomains {
			break
		}
		fmt.Printf("%2d) %-35s %5d messages\n", idx+1, bucket.Domain, bucket.Count)
	}

	plannedActions := make([]phase3Action, 0)
	stoppedEarly := false

	for idx, bucket := range ordered {
		if idx >= maxDomains {
			break
		}

		choice := r.domainChoiceForBucket(idx, maxDomains, bucket)

		switch choice {
		case "l":
			selectedBucket := bucket
			plannedActions = append(plannedActions, phase3Action{Description: fmt.Sprintf("Label+archive domain %s", selectedBucket.Domain), Run: func(runCtx context.Context) error { return r.labelAndArchiveDomain(runCtx, selectedBucket) }})
			fmt.Printf("Queued action: label+archive %s.\n", selectedBucket.Domain)
		case "u":
			selectedBucket := bucket
			plannedActions = append(plannedActions, phase3Action{Description: fmt.Sprintf("Unsubscribe+archive domain %s", selectedBucket.Domain), Run: func(runCtx context.Context) error { return r.unsubscribeDomain(runCtx, selectedBucket) }})
			fmt.Printf("Queued action: unsubscribe+archive %s.\n", selectedBucket.Domain)
		case "g":
			if err := r.planUnsubscribeDomainBySender(bucket, &plannedActions); err != nil {
				return err
			}
		case "q":
			fmt.Println("Stopped domain triage early by request.")
			stoppedEarly = true
		default:
			fmt.Println("Skipped.")
		}
		if stoppedEarly {
			break
		}
	}

	if len(plannedActions) == 0 {
		fmt.Println("No phase 3 actions selected.")
		return nil
	}
	return r.applyPhase3ActionPlan(ctx, plannedActions)
}

func (r *Phase3Runner) domainChoiceForBucket(idx, maxDomains int, bucket *domainBucket) string {
	switch r.DomainAction {
	case "label":
		return "l"
	case "unsubscribe":
		return "u"
	case "skip":
		return "s"
	}
	if r.NonInteractive {
		return "s"
	}

	fmt.Printf("\n[%d/%d] %s (%d messages, %d unique senders)\n", idx+1, maxDomains, bucket.Domain, bucket.Count, len(bucket.Senders))
	fmt.Printf("  Latest subject: %q\n", bucket.LatestSubject)
	return r.PromptChoice("Choose: [l]abel+archive [u]nsubscribe-domain [g]ranular-unsubscribe [s]kip [q]uit early", "s", map[string]struct{}{
		"l": {},
		"u": {},
		"g": {},
		"s": {},
		"q": {},
	})
}

func (r *Phase3Runner) validate() error {
	if r.Client == nil || r.PromptChoice == nil {
		return fmt.Errorf("phase 3 runner missing required dependency")
	}
	return nil
}

func (r *Phase3Runner) currentMailboxHistoryID(ctx context.Context) (uint64, error) {
	profile, err := r.Client.Service().Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("get mailbox profile: %w", err)
	}
	return profile.HistoryId, nil
}

func (r *Phase3Runner) changedMessageIDsSince(ctx context.Context, startHistoryID uint64) (map[string]struct{}, bool, error) {
	if startHistoryID == 0 {
		return map[string]struct{}{}, false, nil
	}
	changed := map[string]struct{}{}
	call := r.Client.Service().Users.History.List("me").
		StartHistoryId(startHistoryID).
		HistoryTypes("messageAdded", "messageDeleted", "labelAdded", "labelRemoved").
		Context(ctx)

	for {
		resp, err := call.Do()
		if err != nil {
			if isHistoryGone(err) {
				return map[string]struct{}{}, false, nil
			}
			return nil, false, fmt.Errorf("list mailbox history since %d: %w", startHistoryID, err)
		}

		for _, h := range resp.History {
			addHistoryMessages(changed, h)
		}

		if resp.NextPageToken == "" {
			break
		}
		call = call.PageToken(resp.NextPageToken)
	}
	return changed, true, nil
}

func addHistoryMessages(changed map[string]struct{}, history *gmail.History) {
	if history == nil {
		return
	}

	for _, msg := range history.Messages {
		addHistoryMessage(changed, msg)
	}
	for _, added := range history.MessagesAdded {
		if added != nil {
			addHistoryMessage(changed, added.Message)
		}
	}
	for _, deleted := range history.MessagesDeleted {
		if deleted != nil {
			addHistoryMessage(changed, deleted.Message)
		}
	}
	for _, addedLabel := range history.LabelsAdded {
		if addedLabel != nil {
			addHistoryMessage(changed, addedLabel.Message)
		}
	}
	for _, removedLabel := range history.LabelsRemoved {
		if removedLabel != nil {
			addHistoryMessage(changed, removedLabel.Message)
		}
	}
}

func addHistoryMessage(changed map[string]struct{}, msg *gmail.Message) {
	if msg == nil || msg.Id == "" {
		return
	}
	changed[msg.Id] = struct{}{}
}

func (r *Phase3Runner) labelAndArchiveDomain(ctx context.Context, bucket *domainBucket) error {
	labelName := "Domain/" + bucket.Domain
	labelID, err := r.Client.EnsureLabel(ctx, labelName)
	if err != nil {
		return err
	}

	if err := r.Client.BatchModify(ctx, bucket.MessageIDs, []string{labelID}, []string{"INBOX"}); err != nil {
		return err
	}
	fmt.Printf("Applied label %q and removed INBOX from %d existing messages.\n", labelName, len(bucket.MessageIDs))

	if err := r.Client.EnsureFutureFilter(ctx, "domain "+bucket.Domain, &gmail.FilterCriteria{Query: "from:" + bucket.Domain}, labelID, true); err != nil {
		return err
	}
	return nil
}

func (r *Phase3Runner) applyPhase3ActionPlan(ctx context.Context, actions []phase3Action) error {
	fmt.Printf("\nPhase 3 execution: applying %d planned actions...\n", len(actions))
	reportScanProgress("Phase 3 apply", 0, len(actions))
	for idx, action := range actions {
		fmt.Printf("\nApplying action [%d/%d]: %s\n", idx+1, len(actions), action.Description)
		if err := action.Run(ctx); err != nil {
			return fmt.Errorf("phase 3 action failed (%s): %w", action.Description, err)
		}
		reportScanProgress("Phase 3 apply", idx+1, len(actions))
	}
	return nil
}

func (r *Phase3Runner) unsubscribeDomain(ctx context.Context, bucket *domainBucket) error {
	senders := topSenders(bucket.Senders)
	found := 0
	attempted := 0
	for _, sender := range senders {
		msgID := bucket.SenderSample[sender.Address]
		if msgID == "" {
			continue
		}

		headerFound, senderHTTPAttempts, err := r.unsubscribeSender(ctx, sender.Address, msgID)
		if err != nil {
			return err
		}
		if headerFound {
			found++
		}
		attempted += senderHTTPAttempts
	}

	r.printUnsubscribeSummary(bucket.Domain, found, attempted)

	return r.archiveDomainInboxMessages(ctx, bucket.Domain)
}

func (r *Phase3Runner) planUnsubscribeDomainBySender(bucket *domainBucket, plannedActions *[]phase3Action) error {
	senders := topSenders(bucket.Senders)
	for i, sender := range senders {
		fmt.Printf("  [%d/%d] %s (%d messages)\n", i+1, len(senders), sender.Address, sender.Count)
		fmt.Printf("    Latest subject: %q\n", bucket.SenderLatestSubject[sender.Address])
		choice := r.PromptChoice("  Sender action: [u]nsubscribe+archive [s]kip [q]uit domain", "s", map[string]struct{}{
			"u": {},
			"s": {},
			"q": {},
		})

		switch choice {
		case "u":
			senderAddress := sender.Address
			msgID := bucket.SenderSample[senderAddress]
			if msgID == "" {
				fmt.Printf("No sample message found for sender %s.\n", senderAddress)
				continue
			}

			*plannedActions = append(*plannedActions, phase3Action{
				Description: fmt.Sprintf("Unsubscribe+archive sender %s", senderAddress),
				Run: func(ctx context.Context) error {
					headerFound, senderAttempts, err := r.unsubscribeSender(ctx, senderAddress, msgID)
					if err != nil {
						return err
					}
					r.printSenderUnsubscribeSummary(senderAddress, headerFound, senderAttempts)

					return r.archiveSenderInboxMessages(ctx, senderAddress)
				},
			})
			fmt.Printf("  Queued sender action: unsubscribe+archive %s.\n", senderAddress)
		case "q":
			fmt.Printf("Stopped sender-level triage early for %s.\n", bucket.Domain)
			return nil
		default:
			fmt.Println("  Skipped sender.")
		}
	}
	return nil
}

func (r *Phase3Runner) unsubscribeSender(ctx context.Context, senderAddress, msgID string) (bool, int, error) {
	msg, err := r.Client.GetMetadata(ctx, msgID, []string{"List-Unsubscribe", "List-Unsubscribe-Post", "Subject"})
	if err != nil {
		return false, 0, err
	}

	listHeader := headerValue(msg.Payload.Headers, "List-Unsubscribe")
	if listHeader == "" {
		return false, 0, nil
	}

	endpoints := parseListUnsubscribe(listHeader)
	if len(endpoints) == 0 {
		return true, 0, nil
	}

	postHeader := headerValue(msg.Payload.Headers, "List-Unsubscribe-Post")
	attempted := 0
	for _, endpoint := range endpoints {
		switch {
		case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
			attempted++
			if r.DryRun {
				fmt.Printf("[dry-run] Would attempt HTTP unsubscribe for %s (%s)\n", senderAddress, endpoint)
				continue
			}
			if err := r.hitUnsubscribeEndpoint(ctx, endpoint, postHeader); err != nil {
				fmt.Printf("HTTP unsubscribe failed for %s (%s): %v\n", senderAddress, endpoint, err)
			} else {
				fmt.Printf("HTTP unsubscribe attempted for %s (%s)\n", senderAddress, endpoint)
			}
		case strings.HasPrefix(endpoint, "mailto:"):
			fmt.Printf("Manual mailto unsubscribe for %s: %s\n", senderAddress, endpoint)
		}
	}
	return true, attempted, nil
}

func (r *Phase3Runner) printUnsubscribeSummary(domain string, found, attempted int) {
	if found == 0 {
		fmt.Printf("No List-Unsubscribe headers found for %s.\n", domain)
		return
	}
	if r.DryRun {
		fmt.Printf("Unsubscribe headers found for %d senders. HTTP attempts planned (dry-run): %d\n", found, attempted)
		return
	}
	fmt.Printf("Unsubscribe headers found for %d senders. HTTP attempts: %d\n", found, attempted)
}

func (r *Phase3Runner) printSenderUnsubscribeSummary(senderAddress string, headerFound bool, senderAttempts int) {
	if !headerFound {
		fmt.Printf("No List-Unsubscribe header found for sender %s.\n", senderAddress)
		return
	}
	if r.DryRun {
		fmt.Printf("Sender unsubscribe headers found. HTTP attempts planned (dry-run): %d\n", senderAttempts)
		return
	}
	fmt.Printf("Sender unsubscribe headers found. HTTP attempts: %d\n", senderAttempts)
}

func (r *Phase3Runner) archiveDomainInboxMessages(ctx context.Context, domainStem string) error {
	return r.archiveInboxMessagesByFromQuery(ctx, domainStem, "domain "+domainStem)
}

func (r *Phase3Runner) archiveSenderInboxMessages(ctx context.Context, senderAddress string) error {
	return r.archiveInboxMessagesByFromQuery(ctx, senderAddress, "sender "+senderAddress)
}

func (r *Phase3Runner) archiveInboxMessagesByFromQuery(ctx context.Context, fromTerm, archiveTarget string) error {
	query := fmt.Sprintf("in:inbox from:%s", fromTerm)
	ids, err := r.Client.ListMessageIDs(ctx, query, []string{"INBOX"}, 0)
	if err != nil {
		return fmt.Errorf("list inbox messages for %s: %w", archiveTarget, err)
	}
	if len(ids) == 0 {
		fmt.Printf("No inbox messages found for %s to archive.\n", archiveTarget)
		return nil
	}

	if err := r.Client.BatchModify(ctx, ids, nil, []string{"INBOX"}); err != nil {
		return fmt.Errorf("archive inbox messages for %s: %w", archiveTarget, err)
	}
	if r.DryRun {
		fmt.Printf("[dry-run] Planned archive for %d inbox messages in %s.\n", len(ids), archiveTarget)
	} else {
		fmt.Printf("Archived %d inbox messages for %s by removing INBOX.\n", len(ids), archiveTarget)
	}
	return nil
}

func (r *Phase3Runner) hitUnsubscribeEndpoint(ctx context.Context, endpoint, postHeader string) error {
	var req *http.Request
	var err error
	if strings.Contains(strings.ToLower(postHeader), "one-click") {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("List-Unsubscribe=One-Click"))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func loadPhase3MetadataCache(path string) (*phase3MetadataCache, error) {
	cache := &phase3MetadataCache{Version: phase3CacheVersion, Messages: map[string]phase3CachedMessage{}}
	if path == "" {
		return cache, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cache, nil
		}
		return nil, fmt.Errorf("read phase3 cache: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return cache, nil
	}
	if err := json.Unmarshal(b, cache); err != nil {
		return nil, fmt.Errorf("parse phase3 cache: %w", err)
	}
	if cache.Version != phase3CacheVersion {
		cache.Version = phase3CacheVersion
	}
	if cache.Messages == nil {
		cache.Messages = map[string]phase3CachedMessage{}
	}
	return cache, nil
}

func savePhase3MetadataCache(path string, cache *phase3MetadataCache) error {
	if path == "" || cache == nil {
		return nil
	}
	cache.Version = phase3CacheVersion
	if cache.Messages == nil {
		cache.Messages = map[string]phase3CachedMessage{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create phase3 cache dir: %w", err)
	}
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal phase3 cache: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write phase3 cache: %w", err)
	}
	return nil
}

func buildPhase3CachedMessage(msg *gmail.Message) phase3CachedMessage {
	labelIDs := append([]string(nil), msg.LabelIds...)
	from := headerValue(msg.Payload.Headers, "From")
	address := parseAddress(from)
	domain := ""
	if address != "" {
		domain = domainFromAddress(address)
	}
	return phase3CachedMessage{
		From:     from,
		Subject:  headerValue(msg.Payload.Headers, "Subject"),
		LabelIDs: labelIDs,
		Address:  address,
		Domain:   domain,
	}
}

func (r *Phase3Runner) fetchPhase3MetadataEntries(ctx context.Context, ids []string) (map[string]phase3CachedMessage, error) {
	entries := make(map[string]phase3CachedMessage, len(ids))
	if len(ids) == 0 {
		return entries, nil
	}

	workers := r.Phase3ScanWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > 25 {
		workers = 25
	}
	if workers > len(ids) {
		workers = len(ids)
	}

	fmt.Printf("Phase 3 metadata fetch: %d messages to refresh (workers=%d)\n", len(ids), workers)
	reportScanProgress("Phase 3 metadata fetch", 0, len(ids))

	jobs := make(chan string)
	results := make(chan phase3FetchResult, len(ids))
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				msg, err := r.getMetadataWithRetry(ctx, id, []string{"From", "Subject"}, 4)
				if err != nil {
					results <- phase3FetchResult{ID: id, Err: err}
					continue
				}
				results <- phase3FetchResult{ID: id, Entry: buildPhase3CachedMessage(msg)}
			}
		}()
	}

	go func() {
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	done := 0
	var firstErr error
	for res := range results {
		done++
		if res.Err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch metadata for %s: %w", res.ID, res.Err)
			}
		} else {
			entries[res.ID] = res.Entry
		}
		reportScanProgress("Phase 3 metadata fetch", done, len(ids))
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return entries, nil
}

func (r *Phase3Runner) getMetadataWithRetry(ctx context.Context, id string, headers []string, maxAttempts int) (*gmail.Message, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		msg, err := r.Client.GetMetadata(ctx, id, headers)
		if err == nil {
			return msg, nil
		}
		lastErr = err
		if attempt == maxAttempts || !gmailapiutil.IsRetriable(err) {
			break
		}
		backoff := time.Duration(attempt*attempt) * 200 * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, lastErr
}

func sortBucketsByCount(buckets map[string]*domainBucket) []*domainBucket {
	ordered := make([]*domainBucket, 0, len(buckets))
	for _, bucket := range buckets {
		ordered = append(ordered, bucket)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Count == ordered[j].Count {
			return ordered[i].Domain < ordered[j].Domain
		}
		return ordered[i].Count > ordered[j].Count
	})
	return ordered
}

func topSenders(senderCounts map[string]int) []senderCount {
	out := make([]senderCount, 0, len(senderCounts))
	for sender, count := range senderCounts {
		out = append(out, senderCount{Address: sender, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Address < out[j].Address
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// Keep this error shape check local to phase implementation where history API is consumed.
func isHistoryGone(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == 404
}
