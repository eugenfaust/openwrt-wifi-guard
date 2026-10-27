// Package config stores Telegram settings in a private UCI configuration.
// Enabled controls Telegram delivery, not Wi-Fi monitoring. Callers must
// serialize writes with other UCI writers; this package does not lock LuCI.
package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Settings contains Telegram credentials. Token is deliberately excluded from JSON.
type Settings struct {
	Enabled bool   `json:"enabled"`
	ChatID  string `json:"chat_id"`
	Token   string `json:"-"`
}

// Store uses /etc/config, uci and a five-second command timeout by default.
type Store struct {
	Dir     string
	UCI     string
	Timeout time.Duration
}

const maxConfig = 64 * 1024
const maxOutput = 4096

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,}$`)
var chatPattern = regexp.MustCompile(`^-?[0-9]+$`)
var errInvalid = errors.New("invalid Telegram settings")
var errStore = errors.New("Telegram configuration operation failed")

// PublishedError means the new configuration was renamed into place but its
// directory durability could not be confirmed. Never assume old credentials
// remain authoritative after this error. It deliberately carries no secrets.
type PublishedError struct{}

func (*PublishedError) Error() string {
	return "Telegram configuration published but durability unconfirmed"
}

// Validate permits independently retained credentials when delivery is disabled.
// Chat IDs are validated textually, avoiding machine-integer limits.
func Validate(s Settings) error {
	if len(s.Token) > 512 || len(s.ChatID) > 64 {
		return errInvalid
	}
	if s.Token != "" && !tokenPattern.MatchString(s.Token) {
		return errInvalid
	}
	if s.ChatID != "" && (!chatPattern.MatchString(s.ChatID) || strings.Trim(s.ChatID, "-0") == "") {
		return errInvalid
	}
	if s.Enabled && (s.Token == "" || s.ChatID == "") {
		return errInvalid
	}
	return nil
}

func (s Store) defaults() Store {
	if s.Dir == "" {
		s.Dir = "/etc/config"
	}
	if s.UCI == "" {
		s.UCI = "uci"
	}
	if s.Timeout <= 0 {
		s.Timeout = 5 * time.Second
	}
	return s
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
func checkDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || !owned(info) {
		return errStore
	}
	return nil
}

// readPrivate checks both the pathname and the opened inode. Missing is distinct
// from an unsafe file; a missing configuration does not require an executable.
func readPrivate(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) || info.Size() > maxConfig {
		return nil, nil, errStore
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, errStore
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, nil, errStore
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
	if err != nil || len(data) > maxConfig {
		return nil, nil, errStore
	}
	return data, info, nil
}

type boundedOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutput - b.buf.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (s Store) command(ctx context.Context, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.UCI, args...)
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stdin = strings.NewReader(input)
	var out, stderr boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() == nil && !out.overflow && !stderr.overflow && out.buf.Len() == 0 && stderr.buf.Len() == 0 && len(args) >= 2 && args[len(args)-2] == "get" {
		// libuci removes empty option values. Only credential fields are optional;
		// telegram_enabled remains mandatory, so broken configs still fail closed.
		key := args[len(args)-1]
		var exit *exec.ExitError
		if (key == "wifi-guard.main.telegram_token" || key == "wifi-guard.main.telegram_chat_id") && errors.As(runErr, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
	}
	if runErr != nil || out.overflow || stderr.overflow || stderr.buf.Len() != 0 || ctx.Err() != nil {
		return "", errStore
	}
	return out.buf.String(), nil
}

// Load reads individual options through bounded argv-only UCI invocations.
// Missing files yield disabled empty settings; malformed or missing options in
// an existing file fail closed rather than hiding a broken UCI installation.
func (s Store) Load(ctx context.Context) (Settings, error) {
	s = s.defaults()
	empty := Settings{}
	if ctx.Err() != nil {
		return empty, errStore
	}
	if _, err := os.Lstat(s.Dir); os.IsNotExist(err) {
		return empty, nil
	}
	if checkDir(s.Dir) != nil {
		return empty, errStore
	}
	_, info, err := readPrivate(filepath.Join(s.Dir, "wifi-guard"))
	if err != nil {
		return empty, err
	}
	if info == nil {
		return empty, nil
	}
	fields := []string{"telegram_enabled", "telegram_chat_id", "telegram_token"}
	values := make([]string, len(fields))
	for i, field := range fields {
		value, err := s.command(ctx, "", "-q", "-c", s.Dir, "get", "wifi-guard.main."+field)
		if err != nil {
			return empty, errStore
		}
		// UCI adds one final newline. Embedded/new extra lines are never values.
		value = strings.TrimSuffix(value, "\n")
		if strings.ContainsAny(value, "\r\n\x00") {
			return empty, errStore
		}
		values[i] = value
	}
	if values[0] != "0" && values[0] != "1" {
		return empty, errStore
	}
	settings := Settings{Enabled: values[0] == "1", ChatID: values[1], Token: values[2]}
	if Validate(settings) != nil {
		return empty, errStore
	}
	return settings, nil
}

// Save edits a private staging copy, commits there and atomically renames only
// after UCI succeeds. Original bytes survive command/validation failures. The
// private stage also prevents mode-0644 UCI replacement files from being exposed.
// Unrelated sections/options are preserved by UCI, not reserialized here.
func (s Store) Save(ctx context.Context, settings Settings) error {
	if err := Validate(settings); err != nil {
		return err
	}
	s = s.defaults()
	if ctx.Err() != nil || checkDir(s.Dir) != nil {
		return errStore
	}
	target := filepath.Join(s.Dir, "wifi-guard")
	original, info, err := readPrivate(target)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.Dir, ".wifi-guard-")
	if err != nil {
		return errStore
	}
	defer os.RemoveAll(stage) // Only the directory created above, never a config tree.
	staged := filepath.Join(stage, "wifi-guard")
	deltas := filepath.Join(stage, "deltas")
	if err := os.Mkdir(deltas, 0700); err != nil {
		return errStore
	}
	if err := os.WriteFile(staged, original, 0600); err != nil {
		return errStore
	}
	enabled := "0"
	if settings.Enabled {
		enabled = "1"
	}
	batch := "set wifi-guard.main='wifi-guard'\n" +
		"set wifi-guard.main.telegram_enabled='" + enabled + "'\n" +
		"set wifi-guard.main.telegram_chat_id='" + settings.ChatID + "'\n" +
		"set wifi-guard.main.telegram_token='" + settings.Token + "'\n" +
		"commit wifi-guard\n"
	// -t confines UCI deltas to the staging directory as well as -c config files.
	if output, err := s.command(ctx, batch, "-c", stage, "-t", deltas, "batch"); err != nil || output != "" {
		return errStore
	}
	// UCI may replace the staging inode with a permissively-mode'd regular file.
	stagedInfo, err := os.Lstat(staged)
	if err != nil || !stagedInfo.Mode().IsRegular() || !owned(stagedInfo) || stagedInfo.Size() > maxConfig {
		return errStore
	}
	f, err := os.OpenFile(staged, os.O_RDWR, 0)
	if err != nil {
		return errStore
	}
	actual, statErr := f.Stat()
	if statErr != nil || !os.SameFile(stagedInfo, actual) {
		f.Close()
		return errStore
	}
	chmodErr := f.Chmod(0600)
	syncErr := f.Sync()
	closeErr := f.Close()
	if chmodErr != nil || syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return errStore
	}
	// A zero exit status alone does not prove that UCI applied the batch.
	for _, field := range []struct{ name, value string }{
		{"telegram_enabled", enabled},
		{"telegram_chat_id", settings.ChatID},
		{"telegram_token", settings.Token},
	} {
		value, err := s.command(ctx, "", "-q", "-c", stage, "-t", deltas, "get", "wifi-guard.main."+field.name)
		if err != nil || strings.TrimSuffix(value, "\n") != field.value {
			return errStore
		}
	}
	// Detect ordinary concurrent replacement/edits before publishing. This is not
	// a cross-process lock: callers must coordinate with external UCI writers.
	current, currentInfo, err := readPrivate(target)
	if err != nil || !bytes.Equal(original, current) || (info == nil) != (currentInfo == nil) {
		return errStore
	}
	if info != nil && !os.SameFile(info, currentInfo) {
		return errStore
	}
	if checkDir(s.Dir) != nil {
		return errStore
	}
	if err := os.Rename(staged, target); err != nil {
		return errStore
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return &PublishedError{}
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return &PublishedError{}
	}
	return nil
}
