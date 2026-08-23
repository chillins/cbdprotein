package notify

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"text/template"
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
)

//go:embed messages.tmpl
var messagesTmpl string

var slackTmpl = template.Must(template.New("slack").Parse(messagesTmpl))

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

type (
	groupMsg struct {
		GroupId  string
		ViewURL  string
		Failed   int
		Missing  int
		TimedOut bool
		Targets  []targetMsg
	}

	targetMsg struct {
		Type    string
		Label   string
		Status  string
		Message string
		OK      bool
		Missing bool
	}

	failureMsg struct {
		Type    string
		Label   string
		GroupId string
		Message string
		Time    string
		ViewURL string
	}
)

func (s *Slack) formatGroup(groupId string, st *groupState, timedOut bool) string {
	msg := groupMsg{
		GroupId:  groupId,
		ViewURL:  s.viewURL(groupId),
		TimedOut: timedOut,
		Targets:  make([]targetMsg, 0, len(st.expected)),
	}

	for _, target := range st.expected {
		line := targetMsg{Type: target.Type, Label: target.Label}
		r, ok := st.results[target]
		switch {
		case !ok:
			msg.Missing++
			line.Missing = true
		case r.Status != StatusOk:
			msg.Failed++
			line.Status = r.Status
			line.Message = oneline(r.Message)
		default:
			line.OK = true
			line.Status = r.Status
		}
		msg.Targets = append(msg.Targets, line)
	}

	return renderTmpl("group", msg)
}

func (s *Slack) formatFailure(r Result) string {
	msg := failureMsg{
		Type:    r.Type,
		Label:   r.Label,
		GroupId: r.GroupId,
		Message: oneline(r.Message),
		ViewURL: s.viewURL(r.GroupId),
	}
	if !r.Datetime.IsZero() {
		msg.Time = r.Datetime.Format("15:04:05")
	}
	return renderTmpl("failure", msg)
}

func renderTmpl(name string, data any) string {
	b := &strings.Builder{}
	if err := slackTmpl.ExecuteTemplate(b, name, data); err != nil {
		log.Printf("[notify] failed to render %s template: %v", name, err)
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Slack) viewURL(groupId string) string {
	if s.baseURL == "" || groupId == "" {
		return ""
	}
	// The view uses hash history, so entries live under /#/group/<gid>/.
	return fmt.Sprintf("%s/#/group/%s/index/", s.baseURL, url.PathEscape(groupId))
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
