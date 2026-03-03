package phases

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gmailtriage/internal/gmailclient"
	"gmailtriage/internal/testutil"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type fakeMessage = testutil.FakeMessage

type batchCallView = testutil.BatchCallView

type filterCallView = testutil.FilterCallView

var labels = testutil.Labels

type harness struct {
	t *testing.T

	svc        *gmail.Service
	httpClient *http.Client
	reader     *bufio.Reader

	dryRun            bool
	lookbackDays      int
	nonInteractive    bool
	domainAction      string
	archiveOldPolicy  string
	labelCache        map[string]string
	phase3ScanWorkers int
	phase3CachePath   string
}

func newHarness(t *testing.T, fake *testutil.FakeGmailAPI) *harness {
	t.Helper()
	svc, err := gmail.NewService(
		context.Background(),
		option.WithHTTPClient(fake.Client()),
		option.WithEndpoint(fake.Endpoint()+"/"),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}

	return &harness{
		t:                 t,
		svc:               svc,
		httpClient:        fake.Client(),
		reader:            bufio.NewReader(strings.NewReader("\n")),
		lookbackDays:      90,
		nonInteractive:    true,
		domainAction:      "skip",
		archiveOldPolicy:  "no",
		labelCache:        map[string]string{},
		phase3ScanWorkers: 4,
		phase3CachePath:   filepath.Join(t.TempDir(), "phase3_cache.json"),
	}
}

func (h *harness) phase2Runner() *Phase2Runner {
	client := h.gmailClient()
	return &Phase2Runner{
		LookbackDays: h.lookbackDays,
		Client:       client,
	}
}

func (h *harness) phase3Runner() *Phase3Runner {
	client := h.gmailClient()
	return &Phase3Runner{
		LookbackDays:      h.lookbackDays,
		DryRun:            h.dryRun,
		NonInteractive:    h.nonInteractive,
		DomainAction:      h.domainAction,
		Phase3ScanWorkers: h.phase3ScanWorkers,
		Phase3CachePath:   h.phase3CachePath,
		HTTPClient:        h.httpClient,
		Client:            client,
		PromptChoice:      h.promptChoice,
	}
}

func (h *harness) phase4Runner() *Phase4Runner {
	client := h.gmailClient()
	return &Phase4Runner{
		ArchiveOldPolicy: h.archiveOldPolicy,
		NonInteractive:   h.nonInteractive,
		LookbackDays:     h.lookbackDays,
		Client:           client,
		PromptChoice:     h.promptChoice,
	}
}

func (h *harness) gmailClient() *gmailclient.Client {
	return gmailclient.New(h.svc, gmailclient.Options{
		DryRun:     h.dryRun,
		LabelCache: h.labelCache,
	})
}

func (h *harness) promptChoice(prompt, defaultChoice string, valid map[string]struct{}) string {
	for {
		fmt.Printf("%s ", prompt)
		input, err := h.reader.ReadString('\n')
		if err != nil {
			return defaultChoice
		}
		choice := strings.ToLower(strings.TrimSpace(input))
		if choice == "" {
			return defaultChoice
		}
		if _, ok := valid[choice]; ok {
			return choice
		}
		fmt.Println("Invalid choice.")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out)
}

func parseHistogramDomains(output string) []string {
	re := regexp.MustCompile(`^\s*\d+\)\s+([^\s]+)\s+\d+\s+messages$`)
	lines := strings.Split(output, "\n")
	var out []string
	for _, line := range lines {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func assertEqual[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch\n got: %#v\nwant: %#v", label, got, want)
	}
}

func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func parseUint(input string) uint64 {
	v, _ := strconv.ParseUint(input, 10, 64)
	return v
}
