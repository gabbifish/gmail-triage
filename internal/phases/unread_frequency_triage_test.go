package phases

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gmailtriage/internal/testutil"
)

func TestDomainTriage_LabelAndFilter_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name        string
		messages    []*fakeMessage
		messageIDs  []string
		domainLimit int
		setup       func(h *harness)
		wantLabels  map[string][]string
		wantFilters []filterCallView
		wantBatches []batchCallView
	}{
		{
			name: "label_and_filter",
			messages: []*fakeMessage{
				{ID: "d1", Headers: map[string]string{"From": "a@example.com"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "d2", Headers: map[string]string{"From": "b@example.com"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "d3", Headers: map[string]string{"From": "c@other.com"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "d4", Headers: map[string]string{"From": "promo@example.com"}, Labels: labels("INBOX", "UNREAD", "CATEGORY_PROMOTIONS")},
			},
			messageIDs:  []string{"d1", "d2", "d3", "d4"},
			domainLimit: 1,
			setup: func(h *harness) {
				h.nonInteractive = true
				h.domainAction = "label"
			},
			wantLabels: map[string][]string{
				"d1": {"Domain/example", "UNREAD"},
				"d2": {"Domain/example", "UNREAD"},
				"d3": {"INBOX", "UNREAD"},
				"d4": {"CATEGORY_PROMOTIONS", "Domain/example", "UNREAD"},
			},
			wantFilters: []filterCallView{{
				Query:        "from:example",
				AddLabels:    []string{"Domain/example"},
				RemoveLabels: []string{"INBOX"},
			}},
			wantBatches: []batchCallView{{
				IDs:          []string{"d1", "d2", "d4"},
				AddLabels:    []string{"Domain/example"},
				RemoveLabels: []string{"INBOX"},
			}},
		},
		{
			name: "label_also_creates_filter",
			messages: []*fakeMessage{
				{ID: "s1", Headers: map[string]string{"From": "a@example.com"}, Labels: labels("INBOX", "UNREAD")},
			},
			messageIDs:  []string{"s1"},
			domainLimit: 1,
			setup: func(h *harness) {
				h.nonInteractive = true
				h.domainAction = "label"
			},
			wantLabels: map[string][]string{
				"s1": {"Domain/example", "UNREAD"},
			},
			wantFilters: []filterCallView{{
				Query:        "from:example",
				AddLabels:    []string{"Domain/example"},
				RemoveLabels: []string{"INBOX"},
			}},
			wantBatches: []batchCallView{{
				IDs:          []string{"s1"},
				AddLabels:    []string{"Domain/example"},
				RemoveLabels: []string{"INBOX"},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := testutil.NewFakeGmailAPI(t, tc.messages)
			defer fake.Close()

			h := newHarness(t, fake)
			tc.setup(h)

			if err := h.phase3Runner().Run(context.Background(), tc.domainLimit); err != nil {
				t.Fatalf("phase3 run failed: %v", err)
			}

			assertEqual(t, "message labels", fake.MessageLabelNames(tc.messageIDs...), tc.wantLabels)
			assertEqual(t, "filter calls", fake.FilterCallsView(), tc.wantFilters)
			assertEqual(t, "batch modify calls", fake.BatchCallsView(), tc.wantBatches)
		})
	}
}

func TestDomainTriage_Unsubscribe_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "u1", Headers: map[string]string{"From": "one@example.com", "List-Unsubscribe": "<https://no-op.invalid>"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 5},
		{ID: "u2", Headers: map[string]string{"From": "two@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 4},
		{ID: "u3", Headers: map[string]string{"From": "two@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 3},
		{ID: "u4", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX"), AgeDays: 120},
		{ID: "u5", Headers: map[string]string{"From": "two@example.com"}, Labels: labels("INBOX"), AgeDays: 150},
	})
	defer fake.Close()

	fake.SetMessageHeader("u1", "List-Unsubscribe", fmt.Sprintf("<%s/unsubscribe/one>", fake.Endpoint()))
	fake.SetMessageHeader("u2", "List-Unsubscribe", fmt.Sprintf("<%s/unsubscribe/two>, <mailto:leave@example.com>", fake.Endpoint()))

	h := newHarness(t, fake)
	h.nonInteractive = true
	h.domainAction = "unsubscribe"

	if err := h.phase3Runner().Run(context.Background(), 1); err != nil {
		t.Fatalf("phase3 run failed: %v", err)
	}

	wantHits := []string{"/unsubscribe/one", "/unsubscribe/two"}
	assertEqual(t, "unsubscribe endpoint hits", fake.UnsubscribeHits(), wantHits)
	assertEqual(t, "unsubscribe archives inbox messages", fake.BatchCallsView(), []batchCallView{{
		IDs:          []string{"u1", "u2", "u3", "u4", "u5"},
		AddLabels:    []string{},
		RemoveLabels: []string{"INBOX"},
	}})
}

func TestDomainTriage_Unsubscribe_DryRunDoesNotHitHTTP_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "u1", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 5},
		{ID: "u2", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX"), AgeDays: 130},
	})
	defer fake.Close()

	fake.SetMessageHeader("u1", "List-Unsubscribe", fmt.Sprintf("<%s/unsubscribe/one>", fake.Endpoint()))

	h := newHarness(t, fake)
	h.nonInteractive = true
	h.domainAction = "unsubscribe"
	h.dryRun = true

	output := captureStdout(t, func() {
		if err := h.phase3Runner().Run(context.Background(), 1); err != nil {
			t.Fatalf("phase3 run failed: %v", err)
		}
	})

	assertEqual(t, "unsubscribe endpoint hits", fake.UnsubscribeHits(), []string(nil))
	assertEqual(t, "dry-run makes no batch modify calls", fake.BatchCallsView(), []batchCallView{})
	if !strings.Contains(output, "[dry-run] Would attempt HTTP unsubscribe") {
		t.Fatalf("expected dry-run unsubscribe message in output, got: %q", output)
	}
	if !strings.Contains(output, "[dry-run] Planned archive") {
		t.Fatalf("expected dry-run archive planning message in output, got: %q", output)
	}
}

func TestDomainTriage_GranularSenderUnsubscribe_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "g1", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 5},
		{ID: "g2", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 4},
		{ID: "g3", Headers: map[string]string{"From": "two@example.com"}, Labels: labels("INBOX", "UNREAD"), AgeDays: 3},
		{ID: "g4", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX"), AgeDays: 150},
		{ID: "g5", Headers: map[string]string{"From": "one@example.com"}, Labels: labels("INBOX"), AgeDays: 180},
	})
	defer fake.Close()

	fake.SetMessageHeader("g1", "List-Unsubscribe", fmt.Sprintf("<%s/unsubscribe/one>", fake.Endpoint()))
	fake.SetMessageHeader("g3", "List-Unsubscribe", fmt.Sprintf("<%s/unsubscribe/two>", fake.Endpoint()))

	h := newHarness(t, fake)
	h.nonInteractive = false
	h.domainAction = "ask"
	h.reader = bufio.NewReader(strings.NewReader("g\nu\ns\n"))

	if err := h.phase3Runner().Run(context.Background(), 1); err != nil {
		t.Fatalf("phase3 run failed: %v", err)
	}

	assertEqual(t, "unsubscribe endpoint hits", fake.UnsubscribeHits(), []string{"/unsubscribe/one"})
	assertEqual(t, "sender-level unsubscribe archives only selected sender", fake.BatchCallsView(), []batchCallView{{
		IDs:          []string{"g1", "g2", "g4", "g5"},
		AddLabels:    []string{},
		RemoveLabels: []string{"INBOX"},
	}})
	assertEqual(t, "no sender-level filters created", fake.FilterCallsView(), []filterCallView{})
}

func TestDomainTriage_QueuedExecutionProgress_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "p1", Headers: map[string]string{"From": "a@example.com", "Subject": "Welcome update"}, Labels: labels("INBOX", "UNREAD")},
	})
	defer fake.Close()

	h := newHarness(t, fake)
	h.nonInteractive = false
	h.domainAction = "ask"
	h.reader = bufio.NewReader(strings.NewReader("l\n"))

	output := captureStdout(t, func() {
		if err := h.phase3Runner().Run(context.Background(), 1); err != nil {
			t.Fatalf("phase3 run failed: %v", err)
		}
	})

	if !strings.Contains(output, "Queued action: label+archive example.") {
		t.Fatalf("expected queued action output, got: %q", output)
	}
	if !strings.Contains(output, "Phase 3 execution: applying 1 planned actions...") {
		t.Fatalf("expected execution progress header, got: %q", output)
	}
	if !strings.Contains(output, "Phase 3 apply: 1/1 (100%)") {
		t.Fatalf("expected apply progress output, got: %q", output)
	}

	assertEqual(t, "batch modify calls", fake.BatchCallsView(), []batchCallView{{
		IDs:          []string{"p1"},
		AddLabels:    []string{"Domain/example"},
		RemoveLabels: []string{"INBOX"},
	}})
	assertEqual(t, "filter calls", fake.FilterCallsView(), []filterCallView{{
		Query:        "from:example",
		AddLabels:    []string{"Domain/example"},
		RemoveLabels: []string{"INBOX"},
	}})
}

func TestDomainTriage_SubjectContextInPrompts_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "s1", Headers: map[string]string{"From": "a@example.com", "Subject": "Quarterly Account Update"}, Labels: labels("INBOX", "UNREAD")},
	})
	defer fake.Close()

	h := newHarness(t, fake)
	h.nonInteractive = false
	h.domainAction = "ask"
	h.reader = bufio.NewReader(strings.NewReader("g\nq\nq\n"))

	output := captureStdout(t, func() {
		if err := h.phase3Runner().Run(context.Background(), 1); err != nil {
			t.Fatalf("phase3 run failed: %v", err)
		}
	})

	if !strings.Contains(output, "Latest subject: \"Quarterly Account Update\"") {
		t.Fatalf("expected subject context in prompts, got: %q", output)
	}
	assertEqual(t, "no batch updates after granular quit", fake.BatchCallsView(), []batchCallView{})
	assertEqual(t, "no filter creation after granular quit", fake.FilterCallsView(), []filterCallView{})
}

func TestDomainTriage_MetadataCacheSkipsUnchangedFetches_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "c1", Headers: map[string]string{"From": "a@example.com", "Subject": "Cached subject"}, Labels: labels("INBOX", "UNREAD")},
	})
	defer fake.Close()

	cachePath := filepath.Join(t.TempDir(), "phase3_cache.json")

	h1 := newHarness(t, fake)
	h1.phase3CachePath = cachePath
	h1.nonInteractive = true
	h1.domainAction = "skip"
	if err := h1.phase3Runner().Run(context.Background(), 1); err != nil {
		t.Fatalf("first phase3 run failed: %v", err)
	}
	firstGetCount := fake.GetCallCount()
	assertEqual(t, "first run metadata gets", firstGetCount, 1)

	h2 := newHarness(t, fake)
	h2.phase3CachePath = cachePath
	h2.nonInteractive = true
	h2.domainAction = "skip"
	if err := h2.phase3Runner().Run(context.Background(), 1); err != nil {
		t.Fatalf("second phase3 run failed: %v", err)
	}
	secondGetCount := fake.GetCallCount()
	assertEqual(t, "second run should use cache without extra metadata gets", secondGetCount, firstGetCount)
}

func TestDomainTriage_Histogram_ExpectedOutputs(t *testing.T) {
	tests := []struct {
		name            string
		messages        []*fakeMessage
		wantLineDomains []string
	}{
		{
			name: "descending_frequency",
			messages: []*fakeMessage{
				{ID: "h1", Headers: map[string]string{"From": "a@alpha.com"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "h2", Headers: map[string]string{"From": "b@alpha.com"}, Labels: labels("INBOX", "UNREAD")},
				{ID: "h3", Headers: map[string]string{"From": "c@beta.com"}, Labels: labels("INBOX", "UNREAD")},
			},
			wantLineDomains: []string{"alpha", "beta"},
		},
		{
			name: "last_three_months_only",
			messages: []*fakeMessage{
				{ID: "n1", Headers: map[string]string{"From": "a@recent.com"}, Labels: labels("INBOX", "UNREAD"), OlderThan3Months: false},
				{ID: "o1", Headers: map[string]string{"From": "b@old.com"}, Labels: labels("INBOX", "UNREAD"), OlderThan3Months: true},
			},
			wantLineDomains: []string{"recent"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := testutil.NewFakeGmailAPI(t, tc.messages)
			defer fake.Close()

			h := newHarness(t, fake)
			h.nonInteractive = true
			h.domainAction = "skip"

			output := captureStdout(t, func() {
				if err := h.phase3Runner().Run(context.Background(), 0); err != nil {
					t.Fatalf("phase3 run failed: %v", err)
				}
			})

			gotDomains := parseHistogramDomains(output)
			assertEqual(t, "histogram domain order", gotDomains, tc.wantLineDomains)
		})
	}
}

func TestDomainTriage_QuitEarly_ExpectedOutputs(t *testing.T) {
	fake := testutil.NewFakeGmailAPI(t, []*fakeMessage{
		{ID: "q1", Headers: map[string]string{"From": "a@alpha.com"}, Labels: labels("INBOX", "UNREAD")},
		{ID: "q2", Headers: map[string]string{"From": "b@beta.com"}, Labels: labels("INBOX", "UNREAD")},
	})
	defer fake.Close()

	h := newHarness(t, fake)
	h.nonInteractive = false
	h.domainAction = "ask"
	h.reader = bufio.NewReader(strings.NewReader("q\n"))

	if err := h.phase3Runner().Run(context.Background(), 0); err != nil {
		t.Fatalf("phase3 run failed: %v", err)
	}

	assertEqual(t, "no batch updates after quit", fake.BatchCallsView(), []batchCallView{})
	assertEqual(t, "no filter creation after quit", fake.FilterCallsView(), []filterCallView{})
}
