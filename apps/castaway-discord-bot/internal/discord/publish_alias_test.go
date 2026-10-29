package discord

import (
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/castaway"
)

func TestChannelsByGuildName(t *testing.T) {
	targets := []castaway.PublishTarget{{Name: "Season 51", GuildID: "g1", ChannelID: "c1"}, {Name: "Season 51 (BrainLand)", GuildID: "g2", ChannelID: "c2"}}
	names := func(id string) string { return map[string]string{"g1": "NowThisIsPodracing", "g2": "BrainLand"}[id] }
	for in, want := range map[string]string{"podracing": "c1", "BRAINLAND": "c2", "Podracing/brainland": "c1/c2", "nope": ""} {
		got, ok := channelsByGuildName(in, targets, names)
		if want == "" && ok || want != "" && got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
}
