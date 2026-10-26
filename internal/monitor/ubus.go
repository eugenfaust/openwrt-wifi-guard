package monitor

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Backend interface {
	Discover(context.Context) ([]string, error)
	Clients(context.Context, string) ([]string, error)
	Subscribe(context.Context, []string) (<-chan Notice, <-chan error)
}
type Ubus struct {
	Path    string
	Timeout time.Duration
}

func (u Ubus) output(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, u.Timeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, u.Path, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ubus %s failed: %w", args[0], err)
	}
	return b, nil
}
func (u Ubus) Discover(ctx context.Context) ([]string, error) {
	b, err := u.output(ctx, "list", "hostapd.*")
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range strings.Fields(string(b)) {
		if strings.HasPrefix(s, "hostapd.") && len(s) > len("hostapd.") && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.Strings(out)
	return out, nil
}
func (u Ubus) Clients(ctx context.Context, obj string) ([]string, error) {
	b, err := u.output(ctx, "call", obj, "get_clients")
	if err != nil {
		return nil, err
	}
	return ParseClients(b)
}

// Subscribe owns and reaps its child. Cancellation interrupts idle stdout reads.
func (u Ubus) Subscribe(ctx context.Context, objects []string) (<-chan Notice, <-chan error) {
	events := make(chan Notice, 128)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(done)
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd := exec.CommandContext(child, u.Path, append([]string{"subscribe"}, objects...)...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			done <- err
			return
		}
		if err = cmd.Start(); err != nil {
			done <- err
			return
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 256<<10)
		for scanner.Scan() {
			if e, ok := ParseEvent(scanner.Bytes()); ok {
				select {
				case events <- e:
				case <-ctx.Done():
					cancel()
					_ = cmd.Wait()
					done <- ctx.Err()
					return
				}
			}
		}
		scanErr := scanner.Err()
		cancel()
		waitErr := cmd.Wait()
		if scanErr != nil {
			done <- scanErr
		} else if ctx.Err() != nil {
			done <- ctx.Err()
		} else if waitErr != nil {
			done <- waitErr
		} else {
			done <- fmt.Errorf("subscription ended")
		}
	}()
	return events, done
}
