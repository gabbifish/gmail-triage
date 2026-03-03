package gmailapiutil

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/api/gmail/v1"
)

// EnsureFutureFilter creates a Gmail filter for future matching messages.
// It always applies labelID and optionally removes INBOX when archive is true.
// In dry-run mode it prints the filter payload without calling the API, and it treats
// "already exists" responses as non-fatal so repeated runs remain idempotent.
func EnsureFutureFilter(ctx context.Context, svc *gmail.Service, dryRun bool, filterTarget string, criteria *gmail.FilterCriteria, labelID string, archive bool) error {
	removeLabels := []string{}
	if archive {
		removeLabels = []string{"INBOX"}
	}

	filter := &gmail.Filter{
		Criteria: criteria,
		Action: &gmail.FilterAction{
			AddLabelIds:    []string{labelID},
			RemoveLabelIds: removeLabels,
		},
	}
	if dryRun {
		payload, err := json.MarshalIndent(filter, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal dry-run filter for %s: %w", filterTarget, err)
		}
		fmt.Printf("[dry-run] Would create Gmail filter for %s:\n%s\n", filterTarget, string(payload))
		return nil
	}

	_, err := svc.Users.Settings.Filters.Create("me", filter).Context(ctx).Do()
	if err != nil {
		if IsAlreadyExists(err) {
			fmt.Printf("Similar Gmail filter already exists for %s; continuing.\n", filterTarget)
			return nil
		}
		return fmt.Errorf("create filter for %s: %w", filterTarget, err)
	}
	fmt.Printf("Created future Gmail filter for %s.\n", filterTarget)
	return nil
}
