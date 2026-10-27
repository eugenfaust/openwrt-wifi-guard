package telegram

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
)

const localTimeTimeout = 500 * time.Millisecond

// formatForSend runs only in the delivery worker (or synchronous test caller),
// never Enqueue/the registry owner. Go's time.Local cannot interpret OpenWrt's
// POSIX /etc/TZ -> /tmp/TZ configuration. The router's libc-backed BusyBox date
// can, including DST at the event instant, not merely at the time of delivery.
// Resolve anew for each attempt so timezone changes do not require a restart.
func formatForSend(ctx context.Context, m Message) string {
	if m.Test {
		return Format(m)
	}
	if at, err := routerLocalTime(ctx, m.At); err == nil {
		return formatAt(m, at, "местное")
	}
	// Never label a guessed UTC time as router-local when date is unavailable,
	// fails, times out or returns an unsupported offset. Delivery still proceeds.
	return Format(m)
}

func routerLocalTime(ctx context.Context, at time.Time) (time.Time, error) {
	return routerLocalTimeWithTZFile(ctx, at, "/tmp/TZ")
}

func routerLocalTimeWithTZFile(ctx context.Context, at time.Time, tzPath string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, localTimeTimeout)
	defer cancel()
	// No shell, user-provided arguments, timezone writes or UCI access. -d @...
	// is supported by OpenWrt 24.10 BusyBox date. Supply /tmp/TZ explicitly:
	// daemons need not inherit the interactive shell's TZ environment, and libc
	// builds differ in which timezone file they consult by default.
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	tz, present, err := readRouterTZ(tzPath)
	if err != nil {
		return time.Time{}, err
	}
	cmd := exec.CommandContext(ctx, "date", "-d", "@"+strconv.FormatInt(at.Unix(), 10), "+%z")
	if present {
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "TZ=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "TZ="+tz)
	}
	cmd.WaitDelay = 100 * time.Millisecond
	var output offsetOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return time.Time{}, errors.New("router timezone unavailable")
	}
	offset, err := parseOffset(output.String())
	if err != nil {
		return time.Time{}, err
	}
	return at.In(time.FixedZone("router", offset)), nil
}

// readRouterTZ only reads a small regular file on the router's tmpfs. Nonblocking
// open plus regular-file validation prevents a substituted FIFO/device from
// hanging the delivery worker; no timezone data is written or logged.
func readRouterTZ(path string) (string, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.New("router timezone unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return "", false, errors.New("invalid router timezone file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 257))
	tz := strings.TrimSpace(string(data))
	if err != nil || len(data) > 256 || strings.ContainsAny(tz, "\x00\r\n") {
		return "", false, errors.New("invalid router timezone file")
	}
	return tz, true, nil
}

// Bound subprocess output as well as runtime, including malfunctioning helpers.
// Returning an error stops the stdout copy rather than allocating without limit.
type offsetOutput struct{ strings.Builder }

func (b *offsetOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16 {
		return 0, errors.New("router timezone output too large")
	}
	return b.Builder.Write(p)
}

func parseOffset(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return 0, errors.New("invalid router timezone offset")
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("invalid router timezone offset")
		}
	}
	hours, _ := strconv.Atoi(s[1:3])
	minutes, _ := strconv.Atoi(s[3:5])
	if hours > 23 || minutes > 59 {
		return 0, errors.New("invalid router timezone offset")
	}
	offset := (hours*60 + minutes) * 60
	if s[0] == '-' {
		offset = -offset
	}
	return offset, nil
}
