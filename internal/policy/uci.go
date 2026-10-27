package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
)

type section struct {
	name, kind string
	values     map[string][]string
}
type change struct{ section, key, op, value string }
type ap struct {
	section, ifname string
	external        map[string]bool
	runtimeACL      map[string]bool
	persistentACL   map[string]bool
}
type prepared struct {
	before, after string
	owners        []ownership
	aps           []ap
	publish       func(context.Context) error
	close         func()
}
type backend interface {
	currentHash(context.Context) (string, error)
	prepare(context.Context, journal) (*prepared, error)
	acl(context.Context, string) (map[string]bool, error)
	bans(context.Context, string) (map[string]bool, error)
	control(context.Context, string, string) error
	disconnect(context.Context, string, string) error
}

func safeName(s string) bool {
	if len(s) == 0 || len(s) > 64 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func sectionName(s string) bool { return safeName(s) && !strings.Contains(s, ".") }

// words understands libuci export quoting, including adjacent quoted strings.
func words(s string) ([]string, error) {
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
	for _, c := range s {
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
		if unicode.IsSpace(c) {
			emit()
			continue
		}
		b.WriteRune(c)
		started = true
	}
	if quote != 0 || escape {
		return nil, errors.New("invalid UCI export quoting")
	}
	emit()
	return out, nil
}
func parseExport(b []byte) ([]section, error) {
	var out []section
	current := -1
	for _, line := range strings.Split(string(b), "\n") {
		w, err := words(line)
		if err != nil {
			return nil, err
		}
		if len(w) == 0 {
			continue
		}
		switch w[0] {
		case "package":
			if len(w) != 2 || w[1] != "wireless" {
				return nil, errors.New("unexpected UCI package")
			}
		case "config":
			if len(w) < 2 || len(w) > 3 {
				return nil, errors.New("invalid UCI section")
			}
			name := ""
			if len(w) == 3 {
				name = w[2]
			}
			out = append(out, section{name: name, kind: w[1], values: map[string][]string{}})
			current = len(out) - 1
		case "option", "list":
			if current < 0 || len(w) != 3 {
				return nil, errors.New("invalid UCI option")
			}
			v := out[current].values
			if w[0] == "option" {
				if _, ok := v[w[1]]; ok {
					return nil, errors.New("duplicate UCI option")
				}
				v[w[1]] = []string{w[2]}
			} else {
				v[w[1]] = append(v[w[1]], w[2])
			}
		default:
			return nil, errors.New("unexpected UCI export token")
		}
	}
	return out, nil
}
func one(s section, k string) string { return strings.Join(s.values[k], " ") }
func active(s section) bool {
	return s.kind == "wifi-iface" && one(s, "disabled") != "1" && one(s, "mode") == "ap" && one(s, "multi_ap") != "1" && one(s, "wds") != "1"
}
func aclValues(s section) (map[string]string, error) {
	m := map[string]string{}
	for _, v := range s.values["maclist"] {
		for _, raw := range strings.Fields(v) {
			n, err := normalize(raw)
			if err != nil {
				return nil, errors.New("non-MAC/VLAN maclist unsupported")
			}
			if _, ok := m[n]; ok {
				return nil, errors.New("duplicate maclist entry unsupported")
			}
			m[n] = raw
		}
	}
	return m, nil
}

// merge produces only target-specific list edits and mode changes we own.
func merge(sections []section, j journal) ([]change, []ownership, []ap, error) {
	var changes []change
	var owners []ownership
	var aps []ap
	seen := map[string]bool{}
	keys := make([]string, 0, len(j.Entries))
	for m := range j.Entries {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	for _, s := range sections {
		if !active(s) {
			continue
		}
		if !sectionName(s.name) || seen[s.name] {
			return nil, nil, nil, errors.New("AP requires unique stable named UCI section")
		}
		seen[s.name] = true
		for _, k := range []string{"macfile", "macfile_accept", "macfile_deny", "accept_mac_file", "deny_mac_file"} {
			if one(s, k) != "" {
				return nil, nil, nil, errors.New("external ACL file unsupported")
			}
		}
		for _, option := range s.values["hostapd_options"] {
			key := strings.TrimSpace(strings.SplitN(option, "=", 2)[0])
			if key == "macaddr_acl" || key == "accept_mac_file" || key == "deny_mac_file" {
				return nil, nil, nil, errors.New("external hostapd ACL option unsupported")
			}
		}
		mode := one(s, "macfilter")
		if mode != "" && mode != "disable" && mode != "deny" {
			return nil, nil, nil, errors.New("AP macfilter mode unsupported")
		}
		list, err := aclValues(s)
		if err != nil {
			return nil, nil, nil, err
		}
		if mode != "deny" && len(list) > 0 {
			return nil, nil, nil, errors.New("inactive nonempty maclist unsupported")
		}
		original := mode
		old := map[string]ownership{}
		for _, o := range j.Owners {
			if o.Section == s.name {
				old[o.MAC] = o
				if o.OriginalMode != "deny" {
					original = o.OriginalMode
				}
			}
		}
		if len(old) > 0 && mode != "deny" {
			return nil, nil, nil, errors.New("owned AP mode changed externally")
		}
		external := map[string]bool{}
		for m := range list {
			if _, ok := old[m]; !ok {
				external[m] = true
			}
		}
		count := len(list)
		addedMode := false
		for _, m := range keys {
			v := j.Entries[m]
			o, owned := old[m]
			_, exists := list[m]
			if v.Desired {
				if !exists {
					changes = append(changes, change{s.name, "maclist", "add_list", m})
					count++
					if !owned {
						o = ownership{s.name, m, original}
					}
					owned = true
					addedMode = true
				}
				if owned {
					owners = append(owners, o)
				}
			} else if owned && exists {
				changes = append(changes, change{s.name, "maclist", "del_list", list[m]})
				count--
			}
		}
		if addedMode && mode != "deny" {
			changes = append(changes, change{s.name, "macfilter", "set", "deny"})
		}
		// Revert only our empty -> deny transition, and only when no external entry remains.
		if count == 0 && len(old) > 0 && original != "deny" {
			op := "set"
			if original == "" {
				op = "delete"
			}
			changes = append(changes, change{s.name, "macfilter", op, original})
		}
		persistent := map[string]bool{}
		for m := range list {
			persistent[m] = true
		}
		aps = append(aps, ap{section: s.name, external: external, persistentACL: persistent})
	}
	for _, o := range j.Owners {
		if !seen[o.Section] {
			return nil, nil, nil, errors.New("owned AP missing or inactive; refusing broad restoration")
		}
	}
	if len(aps) == 0 {
		return nil, nil, nil, errors.New("no supported active AP sections")
	}
	return changes, owners, aps, nil
}

type systemBackend struct{ opts Options }

func (b *systemBackend) currentHash(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, _, err := regularConfig(filepath.Join(b.opts.ConfigDir, "wireless"))
	return hash(data), err
}
func (b *systemBackend) prepare(ctx context.Context, j journal) (p *prepared, err error) {
	path := filepath.Join(b.opts.ConfigDir, "wireless")
	before, info, err := regularConfig(path)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(filepath.Dir(b.opts.Path), ".policy-uci-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(root)
		}
	}()
	config := filepath.Join(root, "config")
	delta := filepath.Join(root, "delta")
	if err = os.Mkdir(config, 0700); err != nil {
		return nil, err
	}
	if err = os.Mkdir(delta, 0700); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(config, "wireless"), before, 0600); err != nil {
		return nil, err
	}
	uci := func(args ...string) ([]byte, error) {
		return run(ctx, b.opts.UCI, append([]string{"-c", config, "-t", delta}, args...)...)
	}
	exp, err := uci("export", "wireless")
	if err != nil {
		return nil, err
	}
	sections, err := parseExport(exp)
	if err != nil {
		return nil, err
	}
	changes, owners, aps, err := merge(sections, j)
	if err != nil {
		return nil, err
	}
	// Preflight ALL APs, including read-only command and ban capabilities.
	mapping, err := b.mapping(ctx)
	if err != nil {
		return nil, err
	}
	for i := range aps {
		ifname := mapping[aps[i].section]
		if !safeName(ifname) {
			return nil, errors.New("active AP mapping unavailable")
		}
		aps[i].ifname = ifname
		if err = b.control(ctx, ifname, "PING"); err != nil {
			return nil, err
		}
		if aps[i].runtimeACL, err = b.acl(ctx, ifname); err != nil {
			return nil, err
		}
		accept, acceptErr := b.readACL(ctx, ifname, "ACCEPT_ACL SHOW")
		if acceptErr != nil {
			return nil, acceptErr
		}
		if len(accept) != 0 {
			return nil, errors.New("external runtime accept ACL unsupported")
		}
		caps, capErr := run(ctx, b.opts.UBus, "-v", "list", "hostapd."+ifname)
		if capErr != nil {
			return nil, capErr
		}
		for _, field := range []string{"del_client", "deauth", "reason"} {
			if !strings.Contains(string(caps), "\""+field+"\"") {
				return nil, errors.New("hostapd del_client capabilities unavailable")
			}
		}
	}
	for _, c := range changes {
		key := "wireless." + c.section + "." + c.key
		if c.op != "delete" {
			key += "=" + c.value
		}
		if _, err = uci(c.op, key); err != nil {
			return nil, err
		}
	}
	after := before
	if len(changes) > 0 {
		if _, err = uci("commit", "wireless"); err != nil {
			return nil, err
		}
		after, _, err = regularConfig(filepath.Join(config, "wireless"))
		if err != nil {
			return nil, err
		}
		// Re-export via libuci, verify it is a fixed point of our desired merge.
		exp, err = uci("export", "wireless")
		if err != nil {
			return nil, err
		}
		readback, er := parseExport(exp)
		if er != nil {
			return nil, er
		}
		verify := j
		verify.Owners = owners
		again, _, _, er := merge(readback, verify)
		if er != nil || len(again) > 0 {
			return nil, errors.New("staged policy readback failed")
		}
	}
	beforeHash := hash(before)
	afterHash := hash(after)
	stat := info.Sys().(*syscall.Stat_t)
	p = &prepared{before: beforeHash, after: afterHash, owners: owners, aps: aps, close: func() { os.RemoveAll(root) }}
	p.publish = func(ctx context.Context) error {
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			got, now, err := regularConfig(path)
			if err != nil {
				return err
			}
			st := now.Sys().(*syscall.Stat_t)
			if hash(got) != beforeHash || !os.SameFile(info, now) || now.Mode() != info.Mode() || st.Uid != stat.Uid || st.Gid != stat.Gid {
				return errors.New("wireless changed before publication")
			}
			return nil
		}
		if err := atomicFile(path, after, info.Mode().Perm(), int(stat.Uid), int(stat.Gid), check); err != nil {
			return fmt.Errorf("wireless publication: %w", err)
		}
		h, err := b.currentHash(ctx)
		if err != nil {
			return err
		}
		if h != afterHash {
			return errors.New("wireless publication readback differs")
		}
		return nil
	}
	return p, nil
}
