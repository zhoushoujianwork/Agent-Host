package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestChannelConcurrencyFlagDefaultsAndBounds(t *testing.T) {
	for _, value := range []int{0, 1, 8, 32} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			var args []string
			want := value
			if value == 0 {
				want = 4
			} else {
				args = []string{"--max-concurrent-conversations", fmt.Sprint(value)}
			}
			o, rest, err := parse("dingtalk serve", args, io.Discard)
			if err != nil || len(rest) != 0 || o.maxConcurrentConversations != want {
				t.Fatal("unexpected parsed concurrency", o.maxConcurrentConversations, rest, err)
			}
		})
	}
	for _, value := range []int{-1, 0, 33} {
		if _, _, err := parse("dingtalk serve", []string{"--max-concurrent-conversations", fmt.Sprint(value)}, io.Discard); err == nil || !strings.Contains(err.Error(), "max-concurrent-conversations") {
			t.Fatal("invalid explicit concurrency accepted", value, err)
		}
	}
}

func TestManagedChannelConcurrencyConfigurationReachesArguments(t *testing.T) {
	for _, value := range []int{0, 1, 8, 32} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			layout, config := newServiceFixture(t)
			config.MaxConcurrentConversations = value
			writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
			_, loaded, _, err := loadService(layout.Root)
			if err != nil {
				t.Fatal(err)
			}
			args := loaded.arguments(layout, "serve")
			o, rest, err := parse("dingtalk serve", args[1:], io.Discard)
			want := value
			if want == 0 {
				want = 4
			}
			if err != nil || len(rest) != 0 || o.maxConcurrentConversations != want {
				t.Fatal("service arguments lost configured concurrency", o.maxConcurrentConversations, rest, err)
			}
			if err := Run(context.Background(), []string{"service", "check", "--root", layout.Root}, nil, io.Discard, io.Discard); err != nil {
				t.Fatal("local service check rejected valid concurrency", err)
			}
		})
	}
	for _, value := range []int{-1, 33} {
		t.Run(fmt.Sprintf("invalid-%d", value), func(t *testing.T) {
			layout, config := newServiceFixture(t)
			config.MaxConcurrentConversations = value
			writeBotJSON(t, filepath.Join(layout.Config, "service.json"), config)
			if _, _, _, err := loadService(layout.Root); err == nil || !strings.Contains(err.Error(), "max_concurrent_conversations") {
				t.Fatal("invalid configured concurrency accepted", value, err)
			}
		})
	}
}
