package phases

import (
	"context"
	"fmt"

	"gmailtriage/internal/gmailclient"
)

type Phase4Runner struct {
	ArchiveOldPolicy string
	NonInteractive   bool

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

	ids, err := r.Client.ListMessageIDs(ctx, "in:inbox older_than:3m", []string{"INBOX"}, 0)
	if err != nil {
		return err
	}
	fmt.Printf("Found %d inbox messages older than 3 months.\n", len(ids))
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
	switch {
	case r.ArchiveOldPolicy == "yes":
		return "y"
	case r.ArchiveOldPolicy == "no" || r.NonInteractive:
		return "n"
	default:
		return r.PromptChoice("Archive all inbox mail older than 3 months? [y/N]", "n", map[string]struct{}{
			"y": {},
			"n": {},
		})
	}
}
