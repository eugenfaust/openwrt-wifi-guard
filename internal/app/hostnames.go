package app

import (
	"context"
	"encoding/json"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/hostnames"
)

const hostnameMaxAge = 2 * time.Minute

func (s *Service) hostnameLoop(provider hostnames.Provider, interval time.Duration) {
	defer s.wg.Done()
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		hints, err := provider.Lookup(ctx)
		cancel()
		if err == nil {
			// Copy and validate even injected providers. Cache data never aliases the
			// provider's map and registry pointers never cross the worker boundary.
			cache := make(map[string]hostnames.Hint)
			if len(hints) <= 16384 {
				for raw, hint := range hints {
					mac, err := devices.NormalizeMAC(raw)
					if err == nil && hostnames.ValidName(hint.Name) {
						cache[mac] = hint
					}
				}
			}
			s.hostnameMu.Lock()
			s.hostnameCache = cache
			s.hostnameUntil = time.Now().Add(hostnameMaxAge)
			s.hostnameMu.Unlock()
		}
		// On failure keep the last bounded cache, but let owner-side expiry remove
		// stale hints. Never log DHCP contents or change notification/trust policy.
		if s.ctx.Err() != nil {
			return
		}
		_, _ = s.dispatchDecision(s.ctx, "_hostnames_refresh", json.RawMessage(`{}`))
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.hostnameWake:
		}
	}
}

// enrichHostnames runs only on the registry owner. It neither performs I/O nor
// creates discoveries/history/notifications. Ordinary monitor batching persists
// metadata changes; lease expiry is also checked at every RPC/alert boundary.
func (s *Service) enrichHostnames() {
	now := time.Now()
	s.hostnameMu.RLock()
	defer s.hostnameMu.RUnlock()
	for mac, d := range s.registry.State.Devices {
		name := ""
		if now.Before(s.hostnameUntil) {
			hint := s.hostnameCache[mac]
			if hint.Expires.IsZero() || now.Before(hint.Expires) {
				name = hint.Name
			}
		}
		if d.Hostname != name {
			d.Hostname = name
			s.registry.Dirty = true
		}
	}
}
