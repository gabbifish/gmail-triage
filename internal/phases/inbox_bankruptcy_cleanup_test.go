package phases

import (
	"context"
	"testing"

	"gmailtriage/internal/testutil"
)

func TestOptionalOldMailArchive_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name       string
		archiveOld string
		messages   []*fakeMessage
		messageIDs []string
		wantLabels map[string][]string
	}{
		{
			name:       "archive_enabled",
			archiveOld: "yes",
			messages: []*fakeMessage{
				{ID: "o1", Headers: map[string]string{"From": "a@example.com"}, Labels: labels("INBOX"), OlderThan3Months: true},
				{ID: "o2", Headers: map[string]string{"From": "b@example.com"}, Labels: labels("INBOX"), OlderThan3Months: true},
				{ID: "n1", Headers: map[string]string{"From": "c@example.com"}, Labels: labels("INBOX"), OlderThan3Months: false},
			},
			messageIDs: []string{"o1", "o2", "n1"},
			wantLabels: map[string][]string{
				"o1": {},
				"o2": {},
				"n1": {"INBOX"},
			},
		},
		{
			name:       "archive_disabled",
			archiveOld: "no",
			messages: []*fakeMessage{
				{ID: "o1", Headers: map[string]string{"From": "a@example.com"}, Labels: labels("INBOX"), OlderThan3Months: true},
			},
			messageIDs: []string{"o1"},
			wantLabels: map[string][]string{"o1": {"INBOX"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := testutil.NewFakeGmailAPI(t, tc.messages)
			defer fake.Close()

			h := newHarness(t, fake)
			h.nonInteractive = true
			h.archiveOldPolicy = tc.archiveOld

			if err := h.phase4Runner().Run(context.Background()); err != nil {
				t.Fatalf("phase4 run failed: %v", err)
			}

			assertEqual(t, "message labels", fake.MessageLabelNames(tc.messageIDs...), tc.wantLabels)
		})
	}
}
