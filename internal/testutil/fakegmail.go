package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/gmail/v1"
)

type FakeMessage struct {
	ID               string
	Snippet          string
	Headers          map[string]string
	Attachments      []string
	Labels           map[string]bool
	OlderThan3Months bool
	AgeDays          int
}

type listCall struct {
	Q        string
	LabelIDs []string
}

type getCall struct {
	ID              string
	Format          string
	MetadataHeaders []string
}

type filterCall struct {
	From         string
	Query        string
	AddLabels    []string
	RemoveLabels []string
}

type FakeGmailAPI struct {
	t            testing.TB
	server       *httptest.Server
	mu           sync.Mutex
	messages     map[string]*FakeMessage
	labelsByName map[string]string
	nextLabel    int
	listCalls    []listCall
	getCalls     []getCall
	batchCalls   []gmail.BatchModifyMessagesRequest
	filterCalls  []filterCall
	unsubHits    []string
	historyID    uint64
	historyByID  map[uint64][]string
}

type BatchCallView struct {
	IDs          []string
	AddLabels    []string
	RemoveLabels []string
}

type FilterCallView struct {
	From         string
	Query        string
	AddLabels    []string
	RemoveLabels []string
}

var (
	olderThanDaysPattern  = regexp.MustCompile(`older_than:(\d+)d`)
	olderThanMonthPattern = regexp.MustCompile(`older_than:(\d+)m`)
	newerThanDaysPattern  = regexp.MustCompile(`newer_than:(\d+)d`)
	newerThanMonthPattern = regexp.MustCompile(`newer_than:(\d+)m`)
)

func NewFakeGmailAPI(t testing.TB, msgs []*FakeMessage) *FakeGmailAPI {
	t.Helper()
	f := &FakeGmailAPI{
		t:            t,
		messages:     map[string]*FakeMessage{},
		labelsByName: map[string]string{"INBOX": "INBOX", "UNREAD": "UNREAD"},
		nextLabel:    1,
		historyID:    100,
		historyByID:  map[uint64][]string{},
	}
	for _, msg := range msgs {
		cloned := *msg
		cloned.Headers = cloneHeaders(msg.Headers)
		cloned.Attachments = append([]string(nil), msg.Attachments...)
		cloned.Labels = cloneLabels(msg.Labels)
		f.messages[msg.ID] = &cloned
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *FakeGmailAPI) Close() {
	f.server.Close()
}

func (f *FakeGmailAPI) Client() *http.Client {
	return f.server.Client()
}

func (f *FakeGmailAPI) Endpoint() string {
	return f.server.URL
}

func (f *FakeGmailAPI) PutLabel(name, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labelsByName[name] = id
}

func (f *FakeGmailAPI) SetMessageHeader(id, key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg, ok := f.messages[id]
	if !ok {
		f.t.Fatalf("message %q not found", id)
	}
	if msg.Headers == nil {
		msg.Headers = map[string]string{}
	}
	msg.Headers[key] = value
}

func cloneHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneLabels(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func Labels(ids ...string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func (f *FakeGmailAPI) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/unsubscribe/") {
		f.mu.Lock()
		f.unsubHits = append(f.unsubHits, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/gmail/v1/users/me/messages":
		f.handleListMessages(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/gmail/v1/users/me/messages/batchModify":
		f.handleBatchModify(w, r)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/messages/"):
		f.handleGetMessage(w, r)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/gmail/v1/users/me/labels":
		f.handleListLabels(w)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/gmail/v1/users/me/labels":
		f.handleCreateLabel(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/gmail/v1/users/me/settings/filters":
		f.handleCreateFilter(w, r)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/gmail/v1/users/me/profile":
		f.handleGetProfile(w)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/gmail/v1/users/me/history":
		f.handleListHistory(w, r)
		return
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
}

func (f *FakeGmailAPI) handleListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	labelIDs := append([]string(nil), r.URL.Query()["labelIds"]...)

	f.mu.Lock()
	f.listCalls = append(f.listCalls, listCall{Q: q, LabelIDs: labelIDs})

	ids := make([]string, 0)
	for id, msg := range f.messages {
		if matchesQuery(msg, q, labelIDs) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	f.mu.Unlock()

	type messageRef struct {
		ID string `json:"id,omitempty"`
	}
	resp := struct {
		Messages []*messageRef `json:"messages,omitempty"`
	}{Messages: make([]*messageRef, 0, len(ids))}
	for _, id := range ids {
		resp.Messages = append(resp.Messages, &messageRef{ID: id})
	}

	writeJSON(w, resp)
}

func matchesQuery(msg *FakeMessage, q string, labelIDs []string) bool {
	q = strings.ToLower(q)
	if strings.Contains(q, "in:inbox") && !msg.Labels["INBOX"] {
		return false
	}
	if strings.Contains(q, "is:unread") && !msg.Labels["UNREAD"] {
		return false
	}
	if !matchesAgeQuery(msg, q) {
		return false
	}
	if strings.Contains(q, "has:attachment") && len(msg.Attachments) == 0 {
		return false
	}

	fromTerms := extractFromTerms(q)
	if len(fromTerms) > 0 {
		fromValue := strings.ToLower(msg.Headers["From"])
		for _, term := range fromTerms {
			if !strings.Contains(fromValue, term) {
				return false
			}
		}
	}

	politicalSignals := []string{"paid for by", "authorized by", "federal election commission", "f.e.c.", "not authorized by any candidate"}
	if containsAny(q, politicalSignals) {
		text := strings.ToLower(msg.Snippet + " " + msg.Headers["Subject"] + " " + msg.Headers["From"])
		if !containsAny(text, politicalSignals) {
			return false
		}
	}

	hasICS := hasAttachmentExt(msg.Attachments, ".ics")
	hasVCS := hasAttachmentExt(msg.Attachments, ".vcs")
	if strings.Contains(q, "filename:ics or filename:vcs") || strings.Contains(q, "filename:vcs or filename:ics") {
		if !(hasICS || hasVCS) {
			return false
		}
	} else {
		if strings.Contains(q, "filename:ics") && !hasICS {
			return false
		}
		if strings.Contains(q, "filename:vcs") && !hasVCS {
			return false
		}
	}

	for _, id := range labelIDs {
		if !msg.Labels[id] {
			return false
		}
	}
	return true
}

func matchesAgeQuery(msg *FakeMessage, query string) bool {
	ageDays := messageAgeDays(msg)

	if olderThanDays, ok := findAgeValue(query, olderThanDaysPattern); ok && ageDays <= olderThanDays {
		return false
	}
	if olderThanMonths, ok := findAgeValue(query, olderThanMonthPattern); ok && ageDays <= olderThanMonths*30 {
		return false
	}
	if newerThanDays, ok := findAgeValue(query, newerThanDaysPattern); ok && ageDays > newerThanDays {
		return false
	}
	if newerThanMonths, ok := findAgeValue(query, newerThanMonthPattern); ok && ageDays > newerThanMonths*30 {
		return false
	}
	return true
}

func messageAgeDays(msg *FakeMessage) int {
	if msg.AgeDays > 0 {
		return msg.AgeDays
	}
	if msg.OlderThan3Months {
		return 120
	}
	return 1
}

func findAgeValue(query string, pattern *regexp.Regexp) (int, bool) {
	matches := pattern.FindStringSubmatch(query)
	if len(matches) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func extractFromTerms(q string) []string {
	fields := strings.Fields(strings.ToLower(q))
	out := make([]string, 0)
	for _, field := range fields {
		if !strings.HasPrefix(field, "from:") {
			continue
		}
		term := strings.TrimPrefix(field, "from:")
		term = strings.Trim(term, "()\"")
		if term != "" {
			out = append(out, term)
		}
	}
	return out
}

func hasAttachmentExt(attachments []string, ext string) bool {
	ext = strings.ToLower(ext)
	for _, name := range attachments {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ext) {
			return true
		}
	}
	return false
}

func (f *FakeGmailAPI) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")

	f.mu.Lock()
	msg, ok := f.messages[id]
	if !ok {
		f.mu.Unlock()
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	f.getCalls = append(f.getCalls, getCall{
		ID:              id,
		Format:          r.URL.Query().Get("format"),
		MetadataHeaders: append([]string(nil), r.URL.Query()["metadataHeaders"]...),
	})
	headers := make([]*gmail.MessagePartHeader, 0, len(msg.Headers))
	for k, v := range msg.Headers {
		headers = append(headers, &gmail.MessagePartHeader{Name: k, Value: v})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].Name < headers[j].Name })
	labelIDs := make([]string, 0, len(msg.Labels))
	for label := range msg.Labels {
		labelIDs = append(labelIDs, label)
	}
	sort.Strings(labelIDs)
	snippet := msg.Snippet
	f.mu.Unlock()

	writeJSON(w, &gmail.Message{
		Id:       id,
		Snippet:  snippet,
		LabelIds: labelIDs,
		Payload:  &gmail.MessagePart{Headers: headers},
	})
}

func (f *FakeGmailAPI) handleListLabels(w http.ResponseWriter) {
	f.mu.Lock()
	labels := make([]*gmail.Label, 0, len(f.labelsByName))
	for name, id := range f.labelsByName {
		labels = append(labels, &gmail.Label{Id: id, Name: name})
	}
	f.mu.Unlock()
	sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
	writeJSON(w, &gmail.ListLabelsResponse{Labels: labels})
}

func (f *FakeGmailAPI) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	var req gmail.Label
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	id, ok := f.labelsByName[req.Name]
	if !ok {
		id = fmt.Sprintf("Label_%d", f.nextLabel)
		f.nextLabel++
		f.labelsByName[req.Name] = id
	}
	f.mu.Unlock()

	writeJSON(w, &gmail.Label{Id: id, Name: req.Name})
}

func (f *FakeGmailAPI) handleBatchModify(w http.ResponseWriter, r *http.Request) {
	var req gmail.BatchModifyMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.batchCalls = append(f.batchCalls, req)
	f.historyID++
	f.historyByID[f.historyID] = append([]string(nil), req.Ids...)
	for _, id := range req.Ids {
		msg, ok := f.messages[id]
		if !ok {
			continue
		}
		for _, add := range req.AddLabelIds {
			msg.Labels[add] = true
		}
		for _, rm := range req.RemoveLabelIds {
			delete(msg.Labels, rm)
		}
	}
	f.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (f *FakeGmailAPI) handleCreateFilter(w http.ResponseWriter, r *http.Request) {
	var req gmail.Filter
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	from := ""
	query := ""
	if req.Criteria != nil {
		from = req.Criteria.From
		query = req.Criteria.Query
	}

	f.mu.Lock()
	f.filterCalls = append(f.filterCalls, filterCall{
		From:         from,
		Query:        query,
		AddLabels:    append([]string(nil), req.Action.AddLabelIds...),
		RemoveLabels: append([]string(nil), req.Action.RemoveLabelIds...),
	})
	f.mu.Unlock()

	writeJSON(w, &gmail.Filter{Id: "filter_1"})
}

func (f *FakeGmailAPI) handleGetProfile(w http.ResponseWriter) {
	f.mu.Lock()
	historyID := f.historyID
	f.mu.Unlock()
	writeJSON(w, &gmail.Profile{EmailAddress: "me@example.com", HistoryId: historyID})
}

func (f *FakeGmailAPI) handleListHistory(w http.ResponseWriter, r *http.Request) {
	startRaw := r.URL.Query().Get("startHistoryId")
	start, err := strconv.ParseUint(startRaw, 10, 64)
	if err != nil {
		http.Error(w, "invalid startHistoryId", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	current := f.historyID
	historyByID := make(map[uint64][]string, len(f.historyByID))
	for k, v := range f.historyByID {
		historyByID[k] = append([]string(nil), v...)
	}
	f.mu.Unlock()

	resp := &gmail.ListHistoryResponse{HistoryId: current, History: []*gmail.History{}}
	for hid := start + 1; hid <= current; hid++ {
		ids := historyByID[hid]
		if len(ids) == 0 {
			continue
		}
		h := &gmail.History{Id: hid, Messages: []*gmail.Message{}}
		for _, id := range ids {
			h.Messages = append(h.Messages, &gmail.Message{Id: id})
		}
		resp.History = append(resp.History, h)
	}
	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *FakeGmailAPI) reverseLabelMapLocked() map[string]string {
	reverse := map[string]string{}
	for name, id := range f.labelsByName {
		reverse[id] = name
	}
	return reverse
}

func (f *FakeGmailAPI) MessageLabelNames(ids ...string) map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	reverse := f.reverseLabelMapLocked()
	out := map[string][]string{}
	for _, id := range ids {
		msg := f.messages[id]
		labelNames := make([]string, 0, len(msg.Labels))
		for labelID := range msg.Labels {
			if name, ok := reverse[labelID]; ok {
				labelNames = append(labelNames, name)
			} else {
				labelNames = append(labelNames, labelID)
			}
		}
		sort.Strings(labelNames)
		out[id] = labelNames
	}
	return out
}

func (f *FakeGmailAPI) GetCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.getCalls)
}

func (f *FakeGmailAPI) GetFormats() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.getCalls))
	for _, c := range f.getCalls {
		out = append(out, c.Format)
	}
	sort.Strings(out)
	return out
}

func (f *FakeGmailAPI) UnsubscribeHits() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.unsubHits...)
	sort.Strings(out)
	return out
}

func (f *FakeGmailAPI) FilterCallsView() []FilterCallView {
	f.mu.Lock()
	defer f.mu.Unlock()
	reverse := f.reverseLabelMapLocked()
	out := make([]FilterCallView, 0, len(f.filterCalls))
	for _, c := range f.filterCalls {
		add := make([]string, 0, len(c.AddLabels))
		for _, id := range c.AddLabels {
			if name, ok := reverse[id]; ok {
				add = append(add, name)
			} else {
				add = append(add, id)
			}
		}
		remove := make([]string, len(c.RemoveLabels))
		copy(remove, c.RemoveLabels)
		sort.Strings(add)
		sort.Strings(remove)
		out = append(out, FilterCallView{From: c.From, Query: c.Query, AddLabels: add, RemoveLabels: remove})
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].From + "|" + out[i].Query
		right := out[j].From + "|" + out[j].Query
		return left < right
	})
	return out
}

func (f *FakeGmailAPI) BatchCallsView() []BatchCallView {
	f.mu.Lock()
	defer f.mu.Unlock()
	reverse := f.reverseLabelMapLocked()
	out := make([]BatchCallView, 0, len(f.batchCalls))
	for _, c := range f.batchCalls {
		ids := append([]string(nil), c.Ids...)
		sort.Strings(ids)

		add := make([]string, 0, len(c.AddLabelIds))
		for _, id := range c.AddLabelIds {
			if name, ok := reverse[id]; ok {
				add = append(add, name)
			} else {
				add = append(add, id)
			}
		}
		sort.Strings(add)

		remove := append([]string(nil), c.RemoveLabelIds...)
		sort.Strings(remove)

		out = append(out, BatchCallView{IDs: ids, AddLabels: add, RemoveLabels: remove})
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i].IDs, ",") < strings.Join(out[j].IDs, ",") })
	return out
}
