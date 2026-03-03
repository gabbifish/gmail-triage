package gmailapiutil

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/api/gmail/v1"
)

func EnsureFutureFilter(ctx context.Context, svc *gmail.Service, dryRun bool, filterTarget string, criteria *gmail.FilterCriteria, labelID string) error {
	filter := &gmail.Filter{
		Criteria: criteria,
		Action: &gmail.FilterAction{
			AddLabelIds:    []string{labelID},
			RemoveLabelIds: []string{"INBOX"},
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
