package monitor

import (
	"encoding/json"
	"fmt"
	"openwrt-wifi-guard/internal/devices"
	"sort"
)

type Notice struct{ Kind, MAC, Interface string }

func ParseEvent(line []byte) (Notice, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return Notice{}, false
	}
	// Fail closed on malformed or ambiguous event envelopes.
	if len(msg) != 1 {
		return Notice{}, false
	}
	for kind, raw := range msg {
		normalized := ""
		switch kind {
		case "sta-authorized":
			normalized = "authorized"
		case "disassoc", "deauth", "inactive-deauth":
			normalized = "disconnected"
		default:
			return Notice{}, false
		}
		var data struct {
			Address string `json:"address"`
			Ifname  string `json:"ifname"`
		}
		if json.Unmarshal(raw, &data) != nil || data.Ifname == "" {
			return Notice{}, false
		}
		mac, err := devices.NormalizeMAC(data.Address)
		if err != nil {
			return Notice{}, false
		}
		return Notice{normalized, mac, data.Ifname}, true
	}
	return Notice{}, false
}
func ParseClients(data []byte) ([]string, error) {
	var msg struct {
		Clients map[string]struct {
			Authorized bool `json:"authorized"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if msg.Clients == nil {
		return nil, fmt.Errorf("missing clients map")
	}
	out := []string{}
	for raw, v := range msg.Clients {
		if !v.Authorized {
			continue
		}
		mac, err := devices.NormalizeMAC(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid client MAC")
		}
		out = append(out, mac)
	}
	sort.Strings(out)
	return out, nil
}
