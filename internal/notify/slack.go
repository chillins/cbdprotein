package notify

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
)

const (
	EnvWebhookURL   = "CBDPROTEIN_SLACK_WEBHOOK_URL"
	EnvGroupTimeout = "CBDPROTEIN_SLACK_GROUP_TIMEOUT"

	StatusOk   = "ok"
	StatusFail = "fail"

	defaultBaseURL      = "http://localhost:9000"
	defaultGroupTimeout = 5 * time.Minute
	postTimeout         = 5 * time.Second
	messageLimit        = 300
	reportPrompt        = "*:next: :action: ベンチ結果のログファイルを作成したらagentで`/analyze`を実行しよう！*"
)

type (
	// Target identifies one collection unit within a group.
	Target struct {
		Type  string
		Label string
	}

	// Result is the terminal state of one collection.
	Result struct {
		Type     string
		Label    string
		GroupId  string
		Status   string
		Message  string
		Datetime time.Time
	}

	// Slack posts collection results to an Incoming Webhook. A nil *Slack is a
	// valid no-op receiver, so callers can wire it up unconditionally.
	Slack struct {
		webhookURL string
		baseURL    string
		timeout    time.Duration

		post func([]byte) error

		mu     sync.Mutex
		groups map[string]*groupState
	}

	groupState struct {
		expected []Target
		results  map[Target]Result
		timer    *time.Timer
		sent     bool
	}
)

// NewSlack reads the configuration from the environment. It returns nil when
// the webhook URL is unset, which disables notification entirely.
func NewSlack() *Slack {
	webhookURL := os.Getenv(EnvWebhookURL)
	if webhookURL == "" {
		log.Printf("[notify] %v is not set; slack notification is disabled", EnvWebhookURL)
		return nil
	}

	timeout := defaultGroupTimeout
	if raw := os.Getenv(EnvGroupTimeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Printf("[notify] failed to parse %v=%q: %v; using %v", EnvGroupTimeout, raw, err, timeout)
		} else {
			timeout = parsed
		}
	}

	s := &Slack{
		webhookURL: webhookURL,
		baseURL:    defaultBaseURL,
		timeout:    timeout,

		groups: map[string]*groupState{},
	}
	s.post = s.postWebhook

	return s
}

// Expect registers the targets a group collection is waiting for. The summary
// is sent once every target has reported, or when the timeout elapses.
func (s *Slack) Expect(groupId string, targets []Target) {
	if s == nil || groupId == "" {
		return
	}

	expected := dedupe(targets)
	if len(expected) == 0 {
		return
	}

	st := &groupState{
		expected: expected,
		results:  map[Target]Result{},
	}
	st.timer = time.AfterFunc(s.timeout, func() { s.flush(groupId, true) })

	s.mu.Lock()
	defer s.mu.Unlock()

	s.groups[groupId] = st
}

// Report records the terminal state of one collection. Failures are notified
// immediately; successes only contribute to their group summary.
func (s *Slack) Report(r Result) {
	if s == nil {
		return
	}

	if r.Status != StatusOk {
		s.send(s.formatFailure(r))
	}

	if r.GroupId == "" {
		return
	}
	target := Target{Type: r.Type, Label: r.Label}

	s.mu.Lock()
	st, ok := s.groups[r.GroupId]
	if !ok || st.sent || !contains(st.expected, target) {
		s.mu.Unlock()
		return
	}
	st.results[target] = r
	complete := len(st.results) >= len(st.expected)
	s.mu.Unlock()

	if complete {
		s.flush(r.GroupId, false)
	}
}

func (s *Slack) flush(groupId string, timedOut bool) {
	s.mu.Lock()
	st, ok := s.groups[groupId]
	if !ok || st.sent {
		s.mu.Unlock()
		return
	}
	st.sent = true
	if st.timer != nil {
		st.timer.Stop()
	}
	delete(s.groups, groupId)
	text := s.formatGroup(groupId, st, timedOut)
	s.mu.Unlock()

	s.send(text)
}

func (s *Slack) formatGroup(groupId string, st *groupState, timedOut bool) string {
	failed, missing := 0, 0
	for _, target := range st.expected {
		r, ok := st.results[target]
		switch {
		case !ok:
			missing++
		case r.Status != StatusOk:
			failed++
		}
	}

	head := "*収集完了*"
	icon := "✅"
	if failed > 0 || missing > 0 {
		head = fmt.Sprintf("収集完了 (失敗 %d / 未着 %d)", failed, missing)
		icon = "⚠️"
	}
	if timedOut {
		head += " ※タイムアウト"
	}

	b := &strings.Builder{}
	fmt.Fprintf(b, "%s cbdprotein %s  group: %s\n", icon, head, groupId)

	for _, target := range st.expected {
		r, ok := st.results[target]
		switch {
		case !ok:
			fmt.Fprintf(b, "• %s / %s — 未着\n", target.Type, target.Label)
		case r.Status == StatusOk:
			fmt.Fprintf(b, "• %s / %s — ok\n", target.Type, target.Label)
		default:
			fmt.Fprintf(b, "• %s / %s — %s: %s\n", target.Type, target.Label, r.Status, oneline(r.Message))
		}
	}

	if link := s.groupLink(groupId); link != "" {
		b.WriteString(link)
		b.WriteByte('\n')
	}
	b.WriteString(reportPrompt)

	return strings.TrimRight(b.String(), "\n")
}

func (s *Slack) formatFailure(r Result) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "❌ cbdprotein 収集失敗  %s / %s", r.Type, r.Label)
	if !r.Datetime.IsZero() {
		fmt.Fprintf(b, "  (%s)", r.Datetime.Format("15:04:05"))
	}
	if r.GroupId != "" {
		fmt.Fprintf(b, "\ngroup: %s", r.GroupId)
	}
	fmt.Fprintf(b, "\n%s", oneline(r.Message))

	if link := s.groupLink(r.GroupId); link != "" {
		fmt.Fprintf(b, "\n%s", link)
	}
	fmt.Fprintf(b, "\n%s", reportPrompt)

	return b.String()
}

func (s *Slack) groupLink(groupId string) string {
	if groupId == "" {
		return ""
	}
	// The view uses hash history, so entries live under /#/group/<gid>/.
	return fmt.Sprintf("<%s/#/group/%s/index/|結果を見る (`ssh -L 9000:localhost:9000 isuconapp`を実行してね)>", s.baseURL, url.PathEscape(groupId))
}

func (s *Slack) send(text string) {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		log.Printf("[notify] failed to serialize payload: %v", err)
		return
	}

	go func() {
		if err := s.post(body); err != nil {
			log.Printf("[notify] failed to post to slack: %v", err)
		}
	}()
}

func (s *Slack) postWebhook(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: postTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http error: status=%v, body=%v", resp.StatusCode, string(content))
	}
	return nil
}

func dedupe(targets []Target) []Target {
	seen := map[Target]bool{}
	res := make([]Target, 0, len(targets))
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		res = append(res, target)
	}
	return res
}

func contains(targets []Target, target Target) bool {
	for _, t := range targets {
		if t == target {
			return true
		}
	}
	return false
}

func oneline(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if runes := []rune(msg); len(runes) > messageLimit {
		return string(runes[:messageLimit]) + "…"
	}
	return msg
}
