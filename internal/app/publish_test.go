package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/storage"
)

func TestPublishedMutationIsNotRolledBackInMemory(t *testing.T) {
	registry := devices.New(10)
	mac := "02:00:00:00:00:01"
	registry.Authorize(mac, "ap0", time.Now())
	registry.Dirty = false
	publishedName := ""
	service, err := New(context.Background(), Options{Registry: registry, Store: &fakeStore{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Save: func(s devices.State) error {
		publishedName = s.Devices[mac].FriendlyName
		return &storage.PublishedError{Err: errors.New("directory fsync failed")}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := request(service, "rename", `{"mac":"02:00:00:00:00:01","name":"Published name"}`); err == nil {
		t.Fatal("unconfirmed durability reported success")
	}
	if publishedName != "Published name" || registry.State.Devices[mac].FriendlyName != publishedName {
		t.Fatal("memory diverged from published file")
	}
	if !registry.Dirty {
		t.Fatal("durability retry lost")
	}
}
