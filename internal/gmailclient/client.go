package gmailclient

import (
	"context"
	"fmt"

	"gmailtriage/internal/gmailapiutil"

	"google.golang.org/api/gmail/v1"
)

const batchModifyChunkSize = 1000

type Options struct {
	DryRun     bool
	LabelCache map[string]string
}

type Client struct {
	svc        *gmail.Service
	dryRun     bool
	labelCache map[string]string
}

// New constructs a Gmail API wrapper used by triage phases.
// The wrapper centralizes dry-run behavior and optional label-name/id cache reuse.
func New(svc *gmail.Service, opts Options) *Client {
	cache := opts.LabelCache
	if cache == nil {
		cache = map[string]string{}
	}
	return &Client{
		svc:        svc,
		dryRun:     opts.DryRun,
		labelCache: cache,
	}
}

// Service exposes the underlying Gmail service for APIs not wrapped by Client helpers.
func (c *Client) Service() *gmail.Service {
	return c.svc
}

// ListMessageIDs returns IDs for messages matching query and optional required labels.
// It paginates through Gmail list responses and stops early when max > 0 and reached.
func (c *Client) ListMessageIDs(ctx context.Context, query string, labelIDs []string, max int) ([]string, error) {
	var ids []string
	call := c.svc.Users.Messages.List("me").Q(query).MaxResults(500).Context(ctx)
	if len(labelIDs) > 0 {
		call = call.LabelIds(labelIDs...)
	}

	for {
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("list messages (%q): %w", query, err)
		}
		for _, msg := range resp.Messages {
			ids = append(ids, msg.Id)
			if max > 0 && len(ids) >= max {
				return ids, nil
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		call = call.PageToken(resp.NextPageToken)
	}
	return ids, nil
}

// GetMetadata fetches a single message in metadata format.
// When headers is provided, Gmail only returns those header names to reduce payload size.
func (c *Client) GetMetadata(ctx context.Context, id string, headers []string) (*gmail.Message, error) {
	call := c.svc.Users.Messages.Get("me", id).Format("metadata").Context(ctx)
	if len(headers) > 0 {
		call = call.MetadataHeaders(headers...)
	}
	msg, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("get message %s: %w", id, err)
	}
	return msg, nil
}

// LookupLabelID returns the Gmail label ID for labelName when present.
// It refreshes the local cache from Gmail if the label is not already cached.
func (c *Client) LookupLabelID(ctx context.Context, labelName string) (string, error) {
	if id, ok := c.labelCache[labelName]; ok {
		return id, nil
	}
	if err := c.hydrateLabelCache(ctx); err != nil {
		return "", err
	}
	return c.labelCache[labelName], nil
}

// EnsureLabel returns a stable label ID for labelName, creating the label if missing.
// In dry-run mode it returns a synthetic ID and logs the intended create action.
func (c *Client) EnsureLabel(ctx context.Context, labelName string) (string, error) {
	if id, ok := c.labelCache[labelName]; ok {
		return id, nil
	}

	if len(c.labelCache) == 0 {
		if err := c.hydrateLabelCache(ctx); err != nil {
			return "", err
		}
		if id, ok := c.labelCache[labelName]; ok {
			return id, nil
		}
	}

	if c.dryRun {
		id := "dryrun/" + labelName
		c.labelCache[labelName] = id
		fmt.Printf("[dry-run] Would create label %q.\n", labelName)
		return id, nil
	}

	newLabel, err := c.svc.Users.Labels.Create("me", &gmail.Label{
		Name:                  labelName,
		LabelListVisibility:   "labelShow",
		MessageListVisibility: "show",
	}).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("create label %q: %w", labelName, err)
	}
	c.labelCache[labelName] = newLabel.Id
	return newLabel.Id, nil
}

// BatchModify adds/removes labels on the provided message IDs.
// It chunks requests to Gmail batch limits and logs intended modifications in dry-run mode.
func (c *Client) BatchModify(ctx context.Context, ids, add, remove []string) error {
	if len(ids) == 0 {
		return nil
	}

	for start := 0; start < len(ids); start += batchModifyChunkSize {
		end := start + batchModifyChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		if c.dryRun {
			fmt.Printf("[dry-run] Would modify %d messages (add=%v remove=%v)\n", len(chunk), add, remove)
			continue
		}

		req := &gmail.BatchModifyMessagesRequest{
			Ids:            chunk,
			AddLabelIds:    add,
			RemoveLabelIds: remove,
		}
		if err := c.svc.Users.Messages.BatchModify("me", req).Context(ctx).Do(); err != nil {
			return fmt.Errorf("batch modify failed: %w", err)
		}
	}
	return nil
}

// EnsureFutureFilter creates a future-mail filter using this client's dry-run setting.
// The filter always applies labelID, and it removes INBOX when archive is true.
func (c *Client) EnsureFutureFilter(ctx context.Context, filterTarget string, criteria *gmail.FilterCriteria, labelID string, archive bool) error {
	return gmailapiutil.EnsureFutureFilter(ctx, c.svc, c.dryRun, filterTarget, criteria, labelID, archive)
}

func (c *Client) hydrateLabelCache(ctx context.Context) error {
	labels, err := c.svc.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("list labels: %w", err)
	}
	for _, label := range labels.Labels {
		c.labelCache[label.Name] = label.Id
	}
	return nil
}
