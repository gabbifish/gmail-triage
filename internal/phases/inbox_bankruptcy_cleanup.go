package phases

import (
	"context"
	"fmt"

	"gmailtriage/internal/gmailclient"
)

type Phase4Runner struct {
	ArchiveOldPolicy string
	NonInteractive   bool
	LookbackDays     int

	Client       *gmailclient.Client
	PromptChoice func(prompt, defaultChoice string, valid map[string]struct{}) string
}

func (r *Phase4Runner) Run(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("phase 4 runner missing gmail client")
	}

	archive := r.archiveDecision()

	if archive != "y" {
		fmt.Println("Skipped older-mail archive.")
		return nil
	}

	lookbackDays := r.LookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 90
	}
	query := fmt.Sprintf("in:inbox older_than:%dd", lookbackDays)
	ids, err := r.Client.ListMessageIDs(ctx, query, []string{"INBOX"}, 0)
	if err != nil {
		return err
	}
	fmt.Printf("Found %d inbox messages older than %d days.\n", len(ids), lookbackDays)
	if len(ids) == 0 {
		return nil
	}

	confirm := "y"
	if r.ArchiveOldPolicy != "yes" && !r.NonInteractive {
		confirm = r.PromptChoice("Confirm archive (remove INBOX) for all of them? [y/N]", "n", map[string]struct{}{
			"y": {},
			"n": {},
		})
	}
	if confirm != "y" {
		fmt.Println("Archive cancelled.")
		return nil
	}

	if err := r.Client.BatchModify(ctx, ids, nil, []string{"INBOX"}); err != nil {
		return err
	}
	fmt.Printf("Archived %d older messages by removing INBOX.\n", len(ids))
	return nil
}

func (r *Phase4Runner) archiveDecision() string {
	lookbackDays := r.LookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 90
	}
	switch {
	case r.ArchiveOldPolicy == "yes":
		return "y"
	case r.ArchiveOldPolicy == "no" || r.NonInteractive:
		return "n"
	default:
		return r.PromptChoice(fmt.Sprintf("Archive all inbox mail older than %d days? [y/N]", lookbackDays), "n", map[string]struct{}{
			"y": {},
			"n": {},
		})
	}
}
