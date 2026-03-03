package phases

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
	"google.golang.org/api/gmail/v1"
)

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func isInboxOnly(labelIDs []string) bool {
	if len(labelIDs) == 0 {
		return false
	}
	foundInbox := false
	for _, label := range labelIDs {
		switch {
		case label == "INBOX":
			foundInbox = true
		case label == "UNREAD", label == "IMPORTANT":
			continue
		case strings.HasPrefix(label, "CATEGORY_"):
			continue
		default:
			return false
		}
	}
	return foundInbox
}

func parseAddress(from string) string {
	if from == "" {
		return ""
	}
	parsed, err := mail.ParseAddress(from)
	if err == nil {
		return strings.ToLower(strings.TrimSpace(parsed.Address))
	}
	if strings.Contains(from, "<") && strings.Contains(from, ">") {
		start := strings.Index(from, "<")
		end := strings.Index(from, ">")
		if start >= 0 && end > start+1 {
			return strings.ToLower(strings.TrimSpace(from[start+1 : end]))
		}
	}
	from = strings.TrimSpace(from)
	if strings.Contains(from, "@") && !strings.Contains(from, " ") {
		return strings.ToLower(from)
	}
	return ""
}

func domainFromAddress(address string) string {
	parts := strings.Split(address, "@")
	if len(parts) != 2 {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parts[1]))
	host = strings.Trim(host, ".")
	if host == "" {
		return ""
	}

	etldPlusOne, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err == nil && etldPlusOne != "" {
		if suffix, _ := publicsuffix.PublicSuffix(etldPlusOne); suffix != "" {
			if stem := strings.TrimSuffix(etldPlusOne, "."+suffix); stem != "" {
				return stem
			}
		}
		labels := strings.Split(etldPlusOne, ".")
		if len(labels) > 0 && labels[0] != "" {
			return labels[0]
		}
	}

	labels := strings.Split(host, ".")
	if len(labels) >= 2 {
		return labels[len(labels)-2]
	}
	return labels[0]
}

func summarizeSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "(no subject)"
	}
	subject = strings.Join(strings.Fields(subject), " ")
	runes := []rune(subject)
	if len(runes) > 100 {
		return string(runes[:97]) + "..."
	}
	return subject
}

func parseListUnsubscribe(header string) []string {
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		candidate = strings.Trim(candidate, "<>")
		if candidate == "" {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			if u.Host != "" {
				out = append(out, candidate)
			}
		case "mailto":
			if u.Opaque != "" || u.Path != "" {
				out = append(out, candidate)
			}
		}
	}
	return out
}

func headerValue(headers []*gmail.MessagePartHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return strings.TrimSpace(h.Value)
		}
	}
	return ""
}

func reportScanProgress(label string, done, total int) {
	if total <= 0 {
		return
	}
	if !shouldReportScanProgress(done, total) {
		return
	}
	pct := 0.0
	if total > 0 {
		pct = float64(done) * 100 / float64(total)
	}
	fmt.Printf("%s: %d/%d (%.0f%%)\n", label, done, total, pct)
}

func shouldReportScanProgress(done, total int) bool {
	if total <= 0 || done < 0 || done > total {
		return false
	}
	if done == 0 || done == total {
		return true
	}
	if total <= 20 {
		return true
	}
	step := total / 20
	if step < 25 {
		step = 25
	}
	return done%step == 0
}
