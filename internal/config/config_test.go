package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_ab-123"

// TestUCIHelper is an isolated fake executable, not a shell implementation of UCI.
func TestUCIHelper(t *testing.T) {
	if os.Getenv("WIFI_GUARD_FAKE_UCI") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	log, _ := os.OpenFile(os.Getenv("FAKE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	json.NewEncoder(log).Encode(args)
	log.Close()
	if strings.Contains(strings.Join(args, " "), testToken) {
		os.Exit(90)
	}
	dir := ""
	deltas := ""
	action := ""
	key := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-t":
			i++
			if args[i-1] == "-c" {
				dir = args[i]
			} else {
				deltas = args[i]
			}
		case "-q":
		default:
			if action == "" {
				action = args[i]
			} else {
				key = args[i]
			}
		}
	}
	if os.Getenv("FAKE_MODE") == "hang" {
		time.Sleep(time.Second * 5)
		os.Exit(1)
	}
	if action == "get" {
		if os.Getenv("FAKE_MODE") == "overflow" {
			fmt.Print(strings.Repeat("x", 100000))
			os.Exit(0)
		}
		if os.Getenv("FAKE_MODE") == "fail" {
			fmt.Fprint(os.Stderr, testToken)
			os.Exit(2)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "wifi-guard"))
		field := strings.TrimPrefix(key, "wifi-guard.main.")
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 3 && parts[0] == "option" && parts[1] == field {
				value := strings.Trim(parts[2], "'")
				if value == "" && os.Getenv("FAKE_MODE") == "empty-is-missing" {
					os.Exit(1)
				}
				fmt.Println(value)
				os.Exit(0)
			}
		}
		os.Exit(1)
	}
	if action != "batch" {
		os.Exit(3)
	}
	stageInfo, err := os.Stat(dir)
	if err != nil || stageInfo.Mode().Perm() != 0700 || deltas == dir || filepath.Dir(deltas) != dir {
		os.Exit(91)
	}
	if info, err := os.Stat(deltas); err != nil || info.Mode().Perm() != 0700 {
		os.Exit(92)
	}
	input, _ := os.ReadFile("/dev/stdin")
	os.WriteFile(os.Getenv("FAKE_LOG")+".stdin", input, 0600)
	path := filepath.Join(dir, "wifi-guard")
	if os.Getenv("FAKE_MODE") == "noop" {
		os.Exit(0)
	}
	if os.Getenv("FAKE_MODE") == "stderr" {
		fmt.Fprint(os.Stderr, testToken)
		os.Exit(0)
	}
	if os.Getenv("FAKE_MODE") == "batch-overflow" {
		fmt.Print(strings.Repeat("x", 100000))
		os.Exit(0)
	}
	if os.Getenv("FAKE_MODE") == "fail" {
		os.WriteFile(path, []byte("corrupted"), 0600)
		fmt.Fprint(os.Stderr, testToken)
		os.Exit(2)
	}
	prior, _ := os.ReadFile(path)
	result := "config wifi-guard 'main'\n"
	for _, line := range strings.Split(string(input), "\n") {
		if strings.HasPrefix(line, "set wifi-guard.main.") {
			pair := strings.SplitN(strings.TrimPrefix(line, "set wifi-guard.main."), "=", 2)
			if len(pair) != 2 {
				os.Exit(4)
			}
			result += " option " + pair[0] + " " + pair[1] + "\n"
		}
	}
	if strings.Contains(string(prior), "keep_me") {
		result += " option keep_me 'yes'\n"
	}
	os.Remove(path)
	os.WriteFile(path, []byte(result), 0644)
	os.Exit(0)
}

func fixture(t *testing.T, mode string) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "fake-uci")
	// Paths originate from testing.T.TempDir and the Go test runner, not user input.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	body := "#!/bin/sh\nexport WIFI_GUARD_FAKE_UCI=1\nexport FAKE_LOG=" + quote(log) + "\nexport FAKE_MODE=" + quote(mode) + "\nexec " + quote(executable) + " -test.run=^TestUCIHelper$ -- \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return Store{Dir: dir, UCI: script, Timeout: 2 * time.Second}, log
}
func seed(t *testing.T, s Store) []byte {
	t.Helper()
	data := []byte("config wifi-guard 'main'\n option telegram_enabled '1'\n option telegram_chat_id '-100123'\n option telegram_token '" + testToken + "'\n option keep_me 'yes'\n")
	if err := os.WriteFile(filepath.Join(s.Dir, "wifi-guard"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidate(t *testing.T) {
	for _, v := range []Settings{{}, {Token: testToken}, {ChatID: "-100123"}, {Enabled: true, Token: testToken, ChatID: "123"}} {
		if err := Validate(v); err != nil {
			t.Fatalf("valid settings rejected: %v", err)
		}
	}
	for _, v := range []Settings{{Enabled: true}, {Token: "x"}, {ChatID: "0"}, {ChatID: "-000"}, {ChatID: "+123"}, {ChatID: "1\nset x=y"}, {Token: "123:short"}, {Token: testToken + "'"}, {Enabled: true, Token: testToken, ChatID: ""}} {
		if err := Validate(v); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	data, _ := json.Marshal(Settings{Token: testToken})
	if strings.Contains(string(data), testToken) {
		t.Fatal("token in JSON")
	}
}
func TestMissingWithoutUCI(t *testing.T) {
	s, _ := fixture(t, "")
	s.UCI = "/does/not/exist"
	got, err := s.Load(context.Background())
	if err != nil || got != (Settings{}) {
		t.Fatalf("missing: %+v %v", got, err)
	}
}
func TestRoundTrip(t *testing.T) {
	s, log := fixture(t, "")
	seed(t, s)
	want := Settings{Enabled: true, ChatID: "-100123", Token: testToken}
	got, err := s.Load(context.Background())
	if err != nil || got != want {
		t.Fatal("load mismatch", err)
	}
	want.Enabled = false
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load(context.Background())
	if err != nil || got != want {
		t.Fatal("round trip mismatch", err)
	}
	info, _ := os.Stat(filepath.Join(s.Dir, "wifi-guard"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("insecure mode")
	}
	data, _ := os.ReadFile(filepath.Join(s.Dir, "wifi-guard"))
	if !strings.Contains(string(data), "keep_me") {
		t.Fatal("unrelated option lost")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), testToken) {
		t.Fatal("token in argv")
	}
	input, _ := os.ReadFile(log + ".stdin")
	if !strings.Contains(string(input), testToken) || !strings.Contains(string(input), "commit wifi-guard\n") {
		t.Fatal("missing batch settings or commit")
	}
}
func TestCreateAndFailures(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s, _ := fixture(t, "")
		if err := s.Save(context.Background(), Settings{}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load(context.Background())
		if err != nil || got != (Settings{}) {
			t.Fatal("create mismatch", err)
		}
	})
	for _, mode := range []string{"fail", "overflow", "hang"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t, mode)
			s.Timeout = 100 * time.Millisecond
			before := seed(t, s)
			if _, err := s.Load(context.Background()); err == nil || strings.Contains(err.Error(), testToken) {
				t.Fatal("load did not fail safely")
			}
			if mode == "overflow" {
				return
			}
			if err := s.Save(context.Background(), Settings{Token: testToken}); err == nil || strings.Contains(err.Error(), testToken) {
				t.Fatal("save did not fail safely")
			}
			after, _ := os.ReadFile(filepath.Join(s.Dir, "wifi-guard"))
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed save modified original")
			}
		})
	}
}
func TestUnsafePaths(t *testing.T) {
	for _, mode := range []string{"file-mode", "directory-mode", "symlink", "directory-symlink"} {
		t.Run(mode, func(t *testing.T) {
			s, log := fixture(t, "")
			seed(t, s)
			path := filepath.Join(s.Dir, "wifi-guard")
			switch mode {
			case "file-mode":
				os.Chmod(path, 0644)
			case "directory-mode":
				os.Chmod(s.Dir, 0770)
			case "symlink":
				os.Rename(path, path+".real")
				os.Symlink(path+".real", path)
			case "directory-symlink":
				alias := s.Dir + "-alias"
				os.Symlink(s.Dir, alias)
				t.Cleanup(func() { os.Remove(alias) })
				s.Dir = alias
			}
			if _, err := s.Load(context.Background()); err == nil {
				t.Fatal("unsafe load accepted")
			}
			if err := s.Save(context.Background(), Settings{}); err == nil {
				t.Fatal("unsafe save accepted")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("executed UCI on unsafe path")
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	s, _ := fixture(t, "")
	seed(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Load(ctx); err == nil {
		t.Fatal("canceled load accepted")
	}
	if err := s.Save(ctx, Settings{}); err == nil {
		t.Fatal("canceled save accepted")
	}
}
func TestSuccessfulButUnappliedBatch(t *testing.T) {
	for _, mode := range []string{"noop", "stderr", "batch-overflow"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t, mode)
			before := seed(t, s)
			if err := s.Save(context.Background(), Settings{}); err == nil {
				t.Fatal("unapplied batch accepted")
			}
			after, _ := os.ReadFile(filepath.Join(s.Dir, "wifi-guard"))
			if !bytes.Equal(before, after) {
				t.Fatal("original changed")
			}
			entries, _ := filepath.Glob(filepath.Join(s.Dir, ".wifi-guard-*"))
			if len(entries) != 0 {
				t.Fatal("staging directory leaked")
			}
		})
	}
}

func TestMalformedAndOversizedConfig(t *testing.T) {
	for _, data := range []string{
		"config wifi-guard 'main'\n",
		"config wifi-guard 'main'\n option telegram_enabled 'maybe'\n option telegram_chat_id ''\n option telegram_token ''\n",
		strings.Repeat("x", maxConfig+1),
	} {
		s, _ := fixture(t, "")
		path := filepath.Join(s.Dir, "wifi-guard")
		os.WriteFile(path, []byte(data), 0600)
		if _, err := s.Load(context.Background()); err == nil {
			t.Fatal("malformed configuration accepted")
		}
	}
}

func TestDefaults(t *testing.T) {
	s := (Store{}).defaults()
	if s.Dir != "/etc/config" || s.UCI != "uci" || s.Timeout != 5*time.Second {
		t.Fatal("incorrect defaults")
	}
}
