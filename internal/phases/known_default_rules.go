package phases

import (
	"context"
	"fmt"

	"gmailtriage/internal/gmailclient"

	"google.golang.org/api/gmail/v1"
)

type Phase2Runner struct {
	LookbackDays int

	Client *gmailclient.Client
}

// Run executes phase 1 known-rule classification for unread inbox mail in the lookback window.
// It applies political and calendar rules to current unread inbox mail, then creates future-mail
// filters where political mail is archived and calendar mail is label-only.
func (r *Phase2Runner) Run(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("phase 2 runner missing gmail client")
	}

	politicalCount, err := r.handlePolitical(ctx)
	if err != nil {
		return err
	}

	calendarCount, err := r.handleCalendar(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Detected political: %d, calendar reminders: %d\n", politicalCount, calendarCount)
	return nil
}

func (r *Phase2Runner) handlePolitical(ctx context.Context) (int, error) {
	politicalQuery := fmt.Sprintf("in:inbox is:unread newer_than:%dd (%s)", r.LookbackDays, PoliticalFilterQuery)
	politicalIDs, err := r.Client.ListMessageIDs(ctx, politicalQuery, nil, 0)
	if err != nil {
		return 0, err
	}
	if err := r.applyRule(ctx, politicalIDs, PoliticalLabelName, "political mail", PoliticalFilterQuery, true); err != nil {
		return 0, err
	}
	return len(politicalIDs), nil
}

func (r *Phase2Runner) handleCalendar(ctx context.Context) (int, error) {
	calendarQuery := fmt.Sprintf("in:inbox is:unread newer_than:%dd %s", r.LookbackDays, CalendarAttachmentFilterQuery)
	calendarIDs, err := r.Client.ListMessageIDs(ctx, calendarQuery, nil, 0)
	if err != nil {
		return 0, err
	}
	if err := r.applyRule(ctx, calendarIDs, CalendarLabelName, "calendar invites", CalendarAttachmentFilterQuery, false); err != nil {
		return 0, err
	}
	return len(calendarIDs), nil
}

func (r *Phase2Runner) applyRule(ctx context.Context, ids []string, labelName, filterTarget, filterQuery string, archiveFuture bool) error {
	if len(ids) == 0 {
		return nil
	}

	labelID, err := r.Client.EnsureLabel(ctx, labelName)
	if err != nil {
		return err
	}

	if err := r.Client.BatchModify(ctx, ids, []string{labelID}, []string{"INBOX"}); err != nil {
		return err
	}
	if err := r.Client.EnsureFutureFilter(ctx, filterTarget, &gmail.FilterCriteria{Query: filterQuery}, labelID, archiveFuture); err != nil {
		return err
	}
	return nil
}
