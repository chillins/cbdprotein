package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
)

const testTimeout = time.Minute

func newTestSlack(timeout time.Duration) (*Slack, chan string) {
	ch := make(chan string, 8)

	s := &Slack{
		webhookURL: "https://example.com/webhook",
		baseURL:    "http://localhost:9000",
		timeout:    timeout,

		groups: map[string]*groupState{},
	}
	s.post = func(body []byte) error {
		payload := map[string]string{}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		ch <- payload["text"]
		return nil
	}

	return s, ch
}

func recvText(t *testing.T, ch chan string) string {
	t.Helper()

	select {
	case text := <-ch:
		return text
	case <-time.After(3 * time.Second):
		t.Fatal("expected a notification, got none")
		return ""
	}
}

func assertQuiet(t *testing.T, ch chan string, d time.Duration) {
	t.Helper()

	select {
	case text := <-ch:
		t.Fatalf("unexpected notification: %v", text)
	case <-time.After(d):
	}
}

func TestGroupSummarySentOnceWhenComplete(t *testing.T) {
	s, ch := newTestSlack(testTimeout)

	s.Expect("grp", []Target{{"pprof", "app"}, {"slowlog", "db1"}})
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk, Message: "Ready"})
	assertQuiet(t, ch, 100*time.Millisecond)

	s.Report(Result{Type: "slowlog", Label: "db1", GroupId: "grp", Status: StatusOk, Message: "Ready"})

	text := recvText(t, ch)
	for _, want := range []string{"✅", "group: grp", "pprof / app — ok", "slowlog / db1 — ok", "/#/group/grp/index/", "/analyze"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary missing %q:\n%v", want, text)
		}
	}

	// A late report for an already-flushed group must not send anything more.
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk, Message: "Ready"})
	assertQuiet(t, ch, 100*time.Millisecond)
}

func TestFailureNotifiedImmediatelyAndInSummary(t *testing.T) {
	s, ch := newTestSlack(testTimeout)

	s.Expect("grp", []Target{{"pprof", "app"}, {"slowlog", "db1"}})
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk, Message: "Ready"})
	s.Report(Result{Type: "slowlog", Label: "db1", GroupId: "grp", Status: StatusFail, Message: "exit\nstatus 1"})

	// The immediate failure and the group summary race; assert on the pair.
	texts := []string{recvText(t, ch), recvText(t, ch)}

	var failure, summary string
	for _, text := range texts {
		if strings.HasPrefix(text, "❌") {
			failure = text
		}
		if strings.HasPrefix(text, "⚠️") {
			summary = text
		}
	}

	if failure == "" {
		t.Fatalf("no immediate failure notification: %v", texts)
	}
	if !strings.Contains(failure, "slowlog / db1") || !strings.Contains(failure, "exit status 1") {
		t.Errorf("unexpected failure notification:\n%v", failure)
	}
	if !strings.Contains(failure, "/analyze") {
		t.Errorf("failure missing report prompt:\n%v", failure)
	}

	if summary == "" {
		t.Fatalf("no group summary: %v", texts)
	}
	for _, want := range []string{"失敗 1 / 未着 0", "pprof / app — ok", "slowlog / db1 — fail: exit status 1"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%v", want, summary)
		}
	}
}

func TestUngroupedReports(t *testing.T) {
	s, ch := newTestSlack(testTimeout)

	// A one-off successful collect is not worth a notification.
	s.Report(Result{Type: "pprof", Label: "app", Status: StatusOk, Message: "Ready"})
	assertQuiet(t, ch, 100*time.Millisecond)

	s.Report(Result{Type: "pprof", Label: "app", Status: StatusFail, Message: "http error"})
	if text := recvText(t, ch); !strings.HasPrefix(text, "❌") {
		t.Errorf("expected a failure notification, got:\n%v", text)
	}
}

func TestUnexpectedTargetDoesNotCompleteGroup(t *testing.T) {
	s, ch := newTestSlack(testTimeout)

	s.Expect("grp", []Target{{"pprof", "app"}})
	// A memo attached to the same group is not one of the expected targets.
	s.Report(Result{Type: "memo", Label: "app", GroupId: "grp", Status: StatusOk})
	assertQuiet(t, ch, 100*time.Millisecond)

	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk})
	if text := recvText(t, ch); !strings.Contains(text, "pprof / app — ok") {
		t.Errorf("unexpected summary:\n%v", text)
	}
}

func TestTimeoutSendsPartialSummary(t *testing.T) {
	s, ch := newTestSlack(50 * time.Millisecond)

	s.Expect("grp", []Target{{"pprof", "app"}, {"slowlog", "db1"}})
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk})

	text := recvText(t, ch)
	for _, want := range []string{"※タイムアウト", "失敗 0 / 未着 1", "pprof / app — ok", "slowlog / db1 — 未着"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary missing %q:\n%v", want, text)
		}
	}

	assertQuiet(t, ch, 100*time.Millisecond)
}

func TestDuplicateTargetsCountedOnce(t *testing.T) {
	s, ch := newTestSlack(testTimeout)

	s.Expect("grp", []Target{{"pprof", "app"}, {"pprof", "app"}})
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusOk})

	if text := recvText(t, ch); strings.Count(text, "pprof / app") != 1 {
		t.Errorf("expected the duplicated target to appear once:\n%v", text)
	}
}

func TestNilSlackIsNoop(t *testing.T) {
	var s *Slack

	s.Expect("grp", []Target{{"pprof", "app"}})
	s.Report(Result{Type: "pprof", Label: "app", GroupId: "grp", Status: StatusFail, Message: "boom"})
}

func TestOnelineTruncatesWithoutSplittingRunes(t *testing.T) {
	msg := oneline(strings.Repeat("あ", messageLimit+10))
	if want := messageLimit + 1; len([]rune(msg)) != want {
		t.Errorf("got %v runes, want %v", len([]rune(msg)), want)
	}
	if !strings.HasSuffix(msg, "…") {
		t.Errorf("expected an ellipsis suffix: %v", msg)
	}
}
