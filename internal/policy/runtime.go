package policy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Never include command output in an error: wireless exports can contain secrets.
type cappedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 2<<20 {
		b.overflow = true
		return n, errors.New("command output limit")
	}
	return b.Buffer.Write(p)
}
func run(parent context.Context, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &cappedBuffer{}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("policy command failed (output suppressed)")
	}
	if out.overflow {
		return nil, errors.New("command output too large")
	}
	return out.Bytes(), nil
}
func (b *systemBackend) mapping(ctx context.Context) (map[string]string, error) {
	data, err := run(ctx, b.opts.UBus, "call", "network.wireless", "status", "{}")
	if err != nil {
		return nil, err
	}
	var radios map[string]struct {
		Up         bool `json:"up"`
		Interfaces []struct {
			Section string `json:"section"`
			Ifname  string `json:"ifname"`
		} `json:"interfaces"`
	}
	if err = json.Unmarshal(data, &radios); err != nil {
		return nil, errors.New("invalid wireless status")
	}
	result := map[string]string{}
	used := map[string]bool{}
	for _, r := range radios {
		if !r.Up {
			continue
		}
		for _, i := range r.Interfaces {
			if !sectionName(i.Section) || !safeName(i.Ifname) {
				return nil, errors.New("unsupported wireless mapping")
			}
			if _, ok := result[i.Section]; ok || used[i.Ifname] {
				return nil, errors.New("ambiguous AP mapping")
			}
			result[i.Section] = i.Ifname
			used[i.Ifname] = true
		}
	}
	return result, nil
}
func (b *systemBackend) exchange(ctx context.Context, ifname, cmd string) (string, error) {
	if !safeName(ifname) {
		return "", errors.New("invalid control interface")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	local := &net.UnixAddr{Name: "@wifi-guard-policy-" + hex.EncodeToString(random[:]), Net: "unixgram"}
	remote := &net.UnixAddr{Name: filepath.Join(b.opts.ControlDir, ifname), Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", local, remote)
	if err != nil {
		return "", errors.New("hostapd control unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return "", err
	}
	// Cancellation closes the socket; join the callback before returning.
	var wg sync.WaitGroup
	wg.Add(1)
	stop := context.AfterFunc(ctx, func() { defer wg.Done(); conn.Close() })
	defer func() {
		if stop() {
			wg.Done()
		}
		wg.Wait()
	}()
	if _, err = conn.Write([]byte(cmd)); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
			return "", context.DeadlineExceeded
		}
		return "", errors.New("hostapd control write failed")
	}
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
			return "", context.DeadlineExceeded
		}
		return "", errors.New("hostapd control read failed")
	}
	if n == len(buf) {
		return "", errors.New("hostapd ACL response too large")
	}
	return strings.TrimSpace(string(buf[:n])), nil
}
func (b *systemBackend) control(ctx context.Context, ifname, command string) error {
	valid := command == "PING"
	for _, prefix := range []string{"DENY_ACL ADD_MAC ", "DENY_ACL DEL_MAC "} {
		if strings.HasPrefix(command, prefix) {
			_, err := normalize(strings.TrimPrefix(command, prefix))
			valid = err == nil
		}
	}
	if !valid {
		return errors.New("control command not permitted")
	}
	reply, err := b.exchange(ctx, ifname, command)
	if err != nil {
		return err
	}
	want := "OK"
	if command == "PING" {
		want = "PONG"
	}
	if reply != want {
		return errors.New("hostapd control operation unsupported or refused")
	}
	return nil
}
func (b *systemBackend) acl(ctx context.Context, ifname string) (map[string]bool, error) {
	return b.readACL(ctx, ifname, "DENY_ACL SHOW")
}
func (b *systemBackend) readACL(ctx context.Context, ifname, command string) (map[string]bool, error) {
	reply, err := b.exchange(ctx, ifname, command)
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 1 && !(len(fields) == 2 && (fields[1] == "VLAN_ID=0" || fields[1] == "0")) {
			return nil, errors.New("VLAN/control ACL format unsupported")
		}
		mac, err := normalize(fields[0])
		if err != nil {
			return nil, errors.New("hostapd deny ACL unsupported")
		}
		m[mac] = true
	}
	return m, nil
}
func (b *systemBackend) bans(ctx context.Context, ifname string) (map[string]bool, error) {
	if !safeName(ifname) {
		return nil, errors.New("invalid AP interface")
	}
	data, err := run(ctx, b.opts.UBus, "call", "hostapd."+ifname, "list_bans", "{}")
	if err != nil {
		return nil, err
	}
	var result struct {
		Clients *[]string `json:"clients"`
	}
	if json.Unmarshal(data, &result) != nil || result.Clients == nil {
		return nil, errors.New("hostapd ban readback unsupported")
	}
	m := map[string]bool{}
	for _, raw := range *result.Clients {
		mac, err := normalize(raw)
		if err != nil {
			return nil, errors.New("invalid ban MAC")
		}
		m[mac] = true
	}
	return m, nil
}
func (b *systemBackend) disconnect(ctx context.Context, ifname, mac string) error {
	if !safeName(ifname) {
		return errors.New("invalid AP interface")
	}
	m, err := normalize(mac)
	if err != nil {
		return err
	}
	request := map[string]any{"addr": m, "deauth": true, "reason": 5}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = run(ctx, b.opts.UBus, "call", "hostapd."+ifname, "del_client", string(data))
	return err
}
