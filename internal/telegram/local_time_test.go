package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeDate(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "date"), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestParseRouterOffset(t *testing.T) {
	for raw, want := range map[string]int{"+0300\n": 10800, "-0330": -12600, "+0545": 20700, "+0000": 0, "-0000": 0} {
		got, err := parseOffset(raw)
		if err != nil || got != want {
			t.Fatalf("%q: got %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "UTC", "+03:00", "+2400", "+0060", "++100", "0300", "+03x0", "+0300\nextra"} {
		if _, err := parseOffset(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestRouterLocalTimePOSIXDSTAtEventInstant(t *testing.T) {
	// No Go zoneinfo dependency: date/libc understands this POSIX TZ string.
	// Resolve old events around both DST transitions even if sent much later.
	t.Setenv("TZ", "UTC0") // The daemon may inherit a stale/different timezone.
	tzPath := filepath.Join(t.TempDir(), "TZ")
	if err := os.WriteFile(tzPath, []byte("CET-1CEST,M3.5.0/2,M10.5.0/3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ utc, want string }{
		{"2026-01-02T03:04:05Z", "2026-01-02 04:04:05 +01:00"},
		{"2026-07-02T03:04:05Z", "2026-07-02 05:04:05 +02:00"},
		{"2026-03-29T00:59:59Z", "2026-03-29 01:59:59 +01:00"},
		{"2026-03-29T01:00:00Z", "2026-03-29 03:00:00 +02:00"},
		{"2026-10-25T00:59:59Z", "2026-10-25 02:59:59 +02:00"},
		{"2026-10-25T01:00:00Z", "2026-10-25 02:00:00 +01:00"},
	} {
		at, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := routerLocalTimeWithTZFile(context.Background(), at, tzPath)
		if err != nil || got.Format("2006-01-02 15:04:05 -07:00") != tc.want || !got.Equal(at) {
			t.Fatalf("%s: %s, %v; want %s", tc.utc, got, err, tc.want)
		}
	}
}

func TestRouterTZFileReadBoundedAndRefreshed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TZ")
	if _, present, err := readRouterTZ(path); err != nil || present {
		t.Fatalf("missing file: %v %v", present, err)
	}
	for _, tz := range []string{"MSK-3", "NPT-5:45"} {
		if err := os.WriteFile(path, []byte(tz+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, present, err := readRouterTZ(path)
		if err != nil || !present || got != tz {
			t.Fatalf("readback %q %v %v", got, present, err)
		}
	}
	for _, bad := range []string{strings.Repeat("a", 257), "UTC0\nMSK-3", "UTC0\x00"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readRouterTZ(path); err == nil {
			t.Fatal("accepted invalid timezone file")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := readRouterTZ(path); err == nil {
		t.Fatal("accepted FIFO")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("FIFO blocked timezone lookup")
	}
}

func TestRouterTimeReadEachSendAndPreservesInstant(t *testing.T) {
	dir := t.TempDir()
	offsetFile := filepath.Join(dir, "offset")
	fakeDate(t, fmt.Sprintf("[ \"$1\" = '-d' ] && [ \"$2\" = '@1767323045' ] && [ \"$3\" = '+%%z' ] || exit 1\nread offset < %q\nprintf '%%s\\n' \"$offset\"", offsetFile))
	at := time.Date(2026, 1, 2, 3, 4, 5, 123, time.UTC)
	for _, offset := range []string{"+0545", "-0330"} {
		if err := os.WriteFile(offsetFile, []byte(offset+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := routerLocalTime(context.Background(), at)
		if err != nil || got.Format("-0700") != offset || !got.Equal(at) {
			t.Fatalf("got %s, %v; want offset %s and original instant", got, err, offset)
		}
	}
}

func TestRouterTimeFailuresFallBackExplicitlyToUTC(t *testing.T) {
	for _, tc := range []struct{ name, script string }{
		{"missing", ""}, {"failure", "exit 1"},
		{"malformed", "printf 'UTC\\n'"},
		{"oversized", "printf '+0300xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'"},
		{"timeout", "while :; do :; done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "missing" {
				t.Setenv("PATH", t.TempDir())
			} else {
				fakeDate(t, tc.script)
			}
			start := time.Now()
			text := formatForSend(context.Background(), Message{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
			if !strings.Contains(text, "Время (UTC; местное время недоступно): 2026-01-02 03:04:05 +00:00") {
				t.Fatalf("misleading fallback: %q", text)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("unbounded timezone lookup")
			}
		})
	}
}

func TestRouterTimeCancellationAndEnqueueDoNotWaitForDate(t *testing.T) {
	fakeDate(t, "while :; do :; done")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := routerLocalTime(ctx, time.Now()); err == nil {
		t.Fatal("ignored canceled context")
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatal("lookup ignored caller deadline")
	}
	n := mustNew(t, Options{Token: syntheticToken, ChatID: "12345"})
	start = time.Now()
	if !n.Enqueue(Message{At: time.Now()}) {
		t.Fatal("enqueue failed")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Enqueue performed timezone I/O")
	}
	if got := formatForSend(context.Background(), Message{Test: true}); !strings.Contains(got, "тестовое уведомление") {
		t.Fatal(got)
	}
}

func TestSendPayloadUsesRouterTime(t *testing.T) {
	fakeDate(t, "printf '+0545\\n'")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if !strings.Contains(payload.Text, "Время (местное): 2026-01-02 08:49:05 +05:45") {
			t.Errorf("wrong payload: %s", payload.Text)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer s.Close()
	n := mustNew(t, Options{Token: syntheticToken, ChatID: "12345", BaseURL: s.URL})
	if err := n.send(context.Background(), Message{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
}
