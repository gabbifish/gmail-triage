package phases

import (
	"context"
	"testing"

	"gmailtriage/internal/testutil"
)

func TestAutoLabelCategories_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name        string
		messages    []*fakeMessage
		messageIDs  []string
		setupFake   func(fake *testutil.FakeGmailAPI)
		wantLabels  map[string][]string
		wantFormat  []string
		wantFilters []filterCallView
	}{
		{
			name: "political_calendar_and_other",
			messages: []*fakeMessage{
				{ID: "p1", Snippet: "Paid for by Friends of Example.", Headers: map[string]string{"From": "campaign@example.org", "Subject": "Update"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "c1", Snippet: "Reminder: Calendar event soon", Headers: map[string]string{"From": "calendar-notification@google.com", "Subject": "Calendar reminder"}, Attachments: []string{"invite.ics"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "n1", Snippet: "Please review attached docs", Headers: map[string]string{"From": "news@sender.com", "Subject": "Weekly updates"}, Labels: labels("INBOX", "UNREAD")},
			},
			messageIDs: []string{"p1", "c1", "n1"},
			wantLabels: map[string][]string{
				"p1": {"Auto/Political", "UNREAD"},
				"c1": {"Auto/Calendar Reminder", "UNREAD"},
				"n1": {"INBOX", "UNREAD"},
			},
			wantFormat: []string{},
			wantFilters: []filterCallView{
				{Query: PoliticalFilterQuery, AddLabels: []string{"Auto/Political"}, RemoveLabels: []string{"INBOX"}},
				{Query: CalendarAttachmentFilterQuery, AddLabels: []string{"Auto/Calendar Reminder"}, RemoveLabels: []string{"INBOX"}},
			},
		},
		{
			name: "already_calendar_labeled_inbox_mail_is_unchanged",
			messages: []*fakeMessage{
				{ID: "c2", Snippet: "Old calendar note", Headers: map[string]string{"From": "misc@sender.com", "Subject": "Calendar"}, Labels: labels("INBOX", "UNREAD", "Auto/Calendar Reminder")},
			},
			messageIDs: []string{"c2"},
			wantLabels: map[string][]string{
				"c2": {"Auto/Calendar Reminder", "INBOX", "UNREAD"},
			},
			wantFormat:  []string{},
			wantFilters: []filterCallView{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := testutil.NewFakeGmailAPI(t, tc.messages)
			if tc.setupFake != nil {
				tc.setupFake(fake)
			}
			defer fake.Close()

			h := newHarness(t, fake)
			h.nonInteractive = true
			if err := h.phase2Runner().Run(context.Background()); err != nil {
				t.Fatalf("phase2 run failed: %v", err)
			}

			assertEqual(t, "message labels", fake.MessageLabelNames(tc.messageIDs...), tc.wantLabels)
			assertEqual(t, "metadata format usage", fake.GetFormats(), tc.wantFormat)
			assertEqual(t, "filter calls", fake.FilterCallsView(), tc.wantFilters)
		})
	}
}
