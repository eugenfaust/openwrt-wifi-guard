// Package hostnames reads optional router-local DHCP metadata, never discovers devices.
package hostnames

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

const maxInput = 1 << 20

var errSource = errors.New("DHCP hostname source unavailable")

// Hint is an untrusted name. Zero Expires means a static assignment or infinite lease.
type Hint struct {
	Name    string
	Expires time.Time
}

// Provider must honor cancellation; implementations run only on a separate worker.
type Provider interface {
	Lookup(context.Context) (map[string]Hint, error)
}

// Local uses only a bounded argv-only UCI export and a regular local lease file.
// Empty fields select OpenWrt defaults; no DNS or external network lookups occur.
type Local struct {
	UCI, ConfigDir, LeaseFile string
	Timeout                   time.Duration
}

func (p Local) Lookup(parent context.Context) (map[string]Hint, error) {
	timeout := p.Timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if p.UCI == "" {
		p.UCI = "uci"
	}
	if p.ConfigDir == "" {
		p.ConfigDir = "/etc/config"
	}
	if p.LeaseFile == "" {
		p.LeaseFile = "/tmp/dhcp.leases"
	}
	cmd := exec.CommandContext(ctx, p.UCI, "-q", "-c", p.ConfigDir, "export", "dhcp")
	cmd.WaitDelay = 100 * time.Millisecond
	var out, stderr cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if cmd.Run() != nil || out.overflow || stderr.overflow || ctx.Err() != nil {
		return nil, errSource
	}
	leases, err := readLeases(p.LeaseFile)
	if err != nil || ctx.Err() != nil {
		return nil, errSource
	}
	return parseSources([]byte(out.String()), leases, time.Now())
}

type cappedOutput struct {
	strings.Builder
	overflow bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > maxInput-b.Len() {
		b.overflow = true
		return 0, errSource
	}
	return b.Builder.Write(p)
}
func readLeases(path string) ([]byte, error) {
	// Open nonblocking before checking the actual inode; a path replacement by a
	// FIFO/device must not stall the worker. O_NOFOLLOW also rejects symlinks.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errSource
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxInput {
		return nil, errSource
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil || len(b) > maxInput {
		return nil, errSource
	}
	return b, nil
}

// ValidName accepts bounded ASCII DNS-style hostnames, never wildcards, control
// characters, markup or shell syntax. DHCP names are display hints, not identity.
func ValidName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	name = strings.TrimSuffix(name, ".")
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func parseSources(uci, leases []byte, now time.Time) (map[string]Hint, error) {
	if len(uci) > maxInput || len(leases) > maxInput {
		return nil, errSource
	}
	result := map[string]Hint{}
	// Resolve duplicate active leases deterministically: latest expiry, then name.
	for _, line := range strings.Split(string(leases), "\n") {
		w := strings.Fields(line)
		if len(w) != 5 || !ValidName(w[3]) {
			continue
		}
		mac, err := devices.NormalizeMAC(w[1])
		if err != nil {
			continue
		}
		unix, err := strconv.ParseInt(w[0], 10, 64)
		if err != nil || unix < 0 || unix != 0 && unix <= now.Unix() {
			continue
		}
		hint := Hint{Name: w[3]}
		if unix != 0 {
			hint.Expires = time.Unix(unix, 0)
		}
		old, ok := result[mac]
		if !ok || !hint.Expires.IsZero() && !old.Expires.IsZero() && hint.Expires.After(old.Expires) || hint.Expires.IsZero() && !old.Expires.IsZero() || hint.Expires.Equal(old.Expires) && hint.Name < old.Name {
			result[mac] = hint
		}
	}
	// First valid static host section in export order wins over leases and later
	// duplicates. Support both option mac (space separated) and list mac forms.
	seen := map[string]bool{}
	kind, name := "", ""
	var macs []string
	flush := func() {
		if kind != "host" || !ValidName(name) {
			return
		}
		for _, raw := range macs {
			mac, err := devices.NormalizeMAC(raw)
			if err == nil && !seen[mac] {
				seen[mac] = true
				result[mac] = Hint{Name: name}
			}
		}
	}
	for _, line := range records(string(uci)) {
		w, err := words(line)
		if err != nil {
			return nil, errSource
		}
		if len(w) == 0 {
			continue
		}
		switch w[0] {
		case "package":
			if len(w) != 2 || w[1] != "dhcp" {
				return nil, errSource
			}
		case "config":
			if len(w) < 2 || len(w) > 3 {
				return nil, errSource
			}
			flush()
			kind = w[1]
			name = ""
			macs = nil
		case "option", "list":
			if len(w) != 3 {
				return nil, errSource
			}
			if w[1] == "name" {
				name = w[2]
			}
			if w[1] == "mac" {
				if w[0] == "option" {
					macs = nil
				}
				macs = append(macs, strings.Fields(w[2])...)
			}
		default:
			return nil, errSource
		}
	}
	flush()
	return result, nil
}

// records splits only outside shell-style quoted values. UCI export may contain
// literal newlines in unrelated options, and comment quotes are not delimiters.
func records(input string) []string {
	var result []string
	var quote byte
	start := 0
	escape, comment := false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if comment {
			if c == '\n' {
				result = append(result, input[start:i])
				start, comment = i+1, false
			}
			continue
		}
		if escape {
			escape = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escape = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			comment = true
		case '\n':
			result = append(result, input[start:i])
			start = i + 1
		}
	}
	if start < len(input) {
		result = append(result, input[start:])
	}
	return result
}

// UCI export uses shell-style quoting, but is parsed as data, never executed.
func words(line string) ([]string, error) {
	var out []string
	var b strings.Builder
	var quote rune
	escape, started := false, false
	emit := func() {
		if started {
			out = append(out, b.String())
			b.Reset()
			started = false
		}
	}
	for _, c := range line {
		if escape {
			b.WriteRune(c)
			escape = false
			started = true
			continue
		}
		if c == '\\' && quote != '\'' {
			escape = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				b.WriteRune(c)
			}
			started = true
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if c == '#' {
			break
		}
		if c == ' ' || c == '\t' || c == '\r' {
			emit()
			continue
		}
		b.WriteRune(c)
		started = true
	}
	if quote != 0 || escape {
		return nil, errSource
	}
	emit()
	return out, nil
}
