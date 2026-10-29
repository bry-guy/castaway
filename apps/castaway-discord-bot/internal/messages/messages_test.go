package messages

import (
	"bytes"
	"strings"
	"testing"
)

// samples holds representative data for every template; a template with no entry fails the test.
var samples = map[string]any{
	"kv":                         map[string]any{"Key": "role", "Value": "x"},
	"labeled":                    map[string]any{"Label": "action", "Value": "x"},
	"more-count":                 map[string]any{"N": 2},
	"ledger-each":                map[string]any{"Key": "k", "Count": 2},
	"episode-label":              3,
	"history-label":              nil,
	"recorded-event":             nil,
	"pings":                      map[string]any{"NotifyUsers": true},
	"truncated-suffix":           nil,
	"draft-truncated":            nil,
	"draft-problem-hint":         nil,
	"admin-alert-action":         map[string]any{"ID": "id", "Revision": "rev"},
	"instance-not-set":           nil,
	"instance-lookup-failed":     map[string]any{"Error": "boom"},
	"instance-missing":           map[string]any{"ID": "id"},
	"instance-with-episode":      map[string]any{"Label": "Season 3", "Episode": "Week 2"},
	"instance-label":             map[string]any{"Season": 3, "Name": "Cagayan"},
	"leaderboard-line":           map[string]any{"Rank": 1, "Prefix": ":lotus: ", "Name": "Ann", "Total": 9, "Draft": 5, "Bonus": 4},
	"error-reply":                "boom",
	"no-leaderboard":             nil,
	"no-instances":               nil,
	"activities-empty":           map[string]any{"Instance": "Season 3"},
	"occurrences-empty":          map[string]any{"Activity": "Immunity"},
	"linked":                     map[string]any{"Name": "Ann", "UserID": "123", "Instance": "Season 3"},
	"unlinked":                   map[string]any{"Name": "Ann", "Instance": "Season 3"},
	"saved-guild-default":        map[string]any{"Instance": "Season 3"},
	"saved-user-default":         map[string]any{"Instance": "Season 3"},
	"cleared-guild-default":      nil,
	"cleared-user-default":       nil,
	"action-hint":                nil,
	"action-recorded":            nil,
	"approval-hint":              nil,
	"approval-revised":           nil,
	"approval-approved":          map[string]any{"When": "Mon Jan 2 3:04pm ET"},
	"not-recorded":               "boom",
	"not-changed":                "boom",
	"not-approved":               "boom",
	"nothing-changed":            "boom",
	"draft-read-failed":          "boom",
	"draft-saved":                map[string]any{"Player": "Ann"},
	"draft-unrecognized":         map[string]any{"Status": "problem"},
	"draft-problem-too-long":     nil,
	"draft-conflict-too-long":    nil,
	"champion-picked":            map[string]any{"Name": "Ann"},
	"champion-not-picked":        "boom",
	"loan-borrowed":              nil,
	"loan-repaid":                nil,
	"setup-error":                map[string]any{"Problem": "Cannot read this channel's configuration.", "Admin": "456"},
	"setup-instance-unavailable": nil,
	"setup-player-not-linked":    nil,
	"setup-channel-config":       nil,
	"setup-channel-verify":       nil,
	"setup-channel-server":       nil,
	"setup-parent-config":        nil,
	"setup-not-bound":            nil,
	"server-channel-only":        nil,
	"command-retired":            nil,
	"unsupported-options":        nil,
	"select-player":              nil,
	"no-draft":                   map[string]any{"Season": 3},
	"no-score":                   map[string]any{"Player": "Ann", "Instance": "Season 3"},
	"points-positive":            nil,
	"tribe-name-required":        nil,
	"activity-name-required":     nil,
	"occurrence-name-required":   nil,
	"user-required":              nil,
	"guild-scope-guild-only":     nil,
	"guild-scope-permission":     nil,
	"season-positive":            nil,
	"season-too-large":           nil,
	"no-instances-season":        map[string]any{"Season": 3},
	"multiple-instances-season":  map[string]any{"Season": 3},
	"instance-ambiguous":         nil,
	"instance-not-in-season":     map[string]any{"Name": "Cagayan", "Season": 3},
	"participant-not-found":      map[string]any{"ID": "id"},
	"not-linked-instance":        nil,
	"not-linked-season":          nil,
	"admin-only":                 map[string]any{"Action": "pot show"},
	"no-match":                   map[string]any{"Kind": "instances", "Query": "x"},
	"multiple-match":             map[string]any{"Kind": "instances", "Query": "x", "Labels": "a, b"},
	"none-found":                 map[string]any{"Kind": "activities"},
	"fix-not-fix":                map[string]any{"Line": "hi"},
	"fix-bad-rank":               map[string]any{"Rank": "0"},
	"fix-duplicate-rank":         map[string]any{"Rank": 7},
	"fix-none":                   nil,
	"access-request.md":          map[string]any{"UserID": "123", "Username": "ann"},
	"approval-prompt.md":         map[string]any{"ChannelID": "c", "When": "Mon", "NotifyUsers": false, "ID": "id", "Revision": "rev"},
	"publish-prompt.md":          map[string]any{"Targets": "- Podracing: <#7002>", "Example": "Podracing", "ID": "id"},
	"publish-posted":             map[string]any{"Names": "Podracing"},
	"publish-cancelled":          nil,
	"publish-closed":             nil,
	"publish-unknown":            map[string]any{"Unknown": "Survivor", "Options": "- Podracing: <#7002>"},
	"publish-ambiguous":          map[string]any{"Options": "- Podracing: <#7002>"},
	"approval-missed.md":         map[string]any{"ChannelID": "c", "When": "Mon", "NotifyUsers": true, "ID": "id", "Revision": "rev"},
	"draft-problem-dm.md":        map[string]any{"Player": "Ann", "Guild": "g", "ChannelID": "c", "MessageID": "m", "Problems": "a\n- b"},
	"draft-conflict.md":          map[string]any{"Mention": "<@1>", "Post": "https://x", "Draft": "1. A", "SavedPicks": "fp"},
	"draft-problem-reply.md":     map[string]any{"Player": "Ann", "Post": "https://x", "Problems": "a", "Draft": "1. A"},
	"score.md":                   map[string]any{"Season": 3, "Line": "1. Ann: 9 (5+4)"},
	"score-private.md":           map[string]any{"Season": 3, "Rank": 1, "Prefix": "", "Name": "Ann", "Total": 9, "Draft": 5, "Bonus": 2, "Secret": 2},
	"leaderboard.md":             map[string]any{"Season": 3, "Lines": []string{"1. Ann"}},
	"draft.md":                   map[string]any{"Season": 3, "Player": "Ann", "Picks": []pick{{Position: 1, ContestantName: "Rob"}}},
	"instances.md":               map[string]any{"Labels": []string{"Season 3"}},
	"activities.md":              map[string]any{"Season": 3, "Activities": []activity{{Name: "Immunity", ActivityType: "t", Status: "s"}}},
	"activity-detail.md": map[string]any{"Season": 3, "Name": "Immunity", "Type": "t", "Status": "s", "Starts": "", "Ends": "",
		"Assignments": []string{"a"}, "Occurrences": []occurrence{{Name: "o", Type: "t", Status: "s", Date: "d"}}},
	"occurrences.md":       map[string]any{"Activity": "Immunity", "Occurrences": []occurrence{{Name: "o", Status: "s", Date: "d", Impacts: []string{"+1 points"}}}},
	"occurrence-detail.md": map[string]any{"Name": "o", "Activity": "a", "Type": "t", "Status": "s", "Date": "d", "Recorded": []string{"r"}, "Impact": []string{"i"}},
	"history.md": map[string]any{"Season": 3, "Name": "Ann", "HasCurrent": true, "Current": "Week 2", "Empty": false,
		"Groups": []group{{Label: "Week 1", Items: []item{{Activity: "Immunity", Details: []string{"d"}}}}}},
	"instance-defaults.md":               map[string]any{"You": "x", "Guild": "y"},
	"stir-the-pot-status.md":             map[string]any{"Season": 3, "Open": true, "Round": "r", "Mine": 1, "Bonus": 2, "Tiers": "4→+2"},
	"stir-the-pot-tribe-status.md":       map[string]any{"Season": 3, "Tribe": "t", "Open": true, "Round": "r", "Contribution": 1, "BonusIfNow": 2, "HasNext": true, "NextLabel": "?→+4", "NextNeeded": 3, "Tiers": "4→+2"},
	"stir-the-pot-contribution.md":       map[string]any{"Season": 3, "Self": false, "Added": 2, "Name": "Ann", "Mine": 2, "Bonus": 1},
	"stir-the-pot-start.md":              map[string]any{"Season": 3, "Round": "r", "Activity": "a"},
	"stir-the-pot-close.md":              map[string]any{"Season": 3, "Round": "r", "Tribes": 2, "Announced": 1},
	"stir-the-pot-close-announcement.md": map[string]any{"Tribe": "t", "Contribution": 1, "Bonus": 2, "Total": 3},
	"auction-status.md": map[string]any{"Season": 3, "Bonus": 1, "LoanDue": 2, "NoLots": false,
		"Lots": []lot{{ContestantName: "Rob", MyBidPoints: 3}}, "Ponies": []lot{{ContestantName: "Rob"}}},
	"auction-bid.md":       map[string]any{"Season": 3, "Self": true, "Contestant": "Rob", "Participant": "Ann", "Bid": 4, "Previous": 1, "Bonus": 2},
	"auction-lot-start.md": map[string]any{"Season": 3, "Contestant": "Rob", "Lot": "Rob"},
	"auction-lot-stop.md":  map[string]any{"Season": 3, "Contestant": "Rob", "Price": 4, "Winning": 4, "HasWinner": true, "Winner": "Ann"},
	"bid-list.md":          map[string]any{"Season": 3, "Bids": []lot{{ContestantName: "Rob", MyBidPoints: 3}}},
	"ponies.md":            map[string]any{"Season": 3, "Ponies": []lot{{ContestantName: "Rob"}}},
	"loan-status.md":       map[string]any{"Season": 3, "Principal": 1, "Interest": 1, "TotalDue": 2, "Remaining": 3, "Bonus": 4, "DueAt": "2026-01-02 15:04 UTC"},
	"loan-action.md":       map[string]any{"Season": 3, "Verb": "Borrowed", "Points": 2, "TotalDue": 2, "Remaining": 3, "Bonus": 4},
	"pony-immunity.md":     map[string]any{"Season": 3, "Name": "Rob", "Created": 2},
	"secret-reveal.md":     map[string]any{"ID": "1", "Name": "Ann", "Count": 2},
}

type pick struct {
	Position       int
	ContestantName string
}

type activity struct {
	Name, ActivityType, Status string
}

type occurrence struct {
	Name, Type, Status, Date string
	Impacts                  []string
}

type group struct {
	Label string
	Items []item
}

type item struct {
	Activity string
	Details  []string
}

type lot struct {
	ContestantName string
	MyBidPoints    int
}

// TestEveryTemplateRenders renders each template (file or {{define}} block) with representative data.
func TestEveryTemplateRenders(t *testing.T) {
	for _, tt := range set.Templates() {
		name := tt.Name()
		if name == "" || strings.HasSuffix(name, ".txt") {
			continue // the root and the .txt containers hold only {{define}} blocks
		}
		data, ok := samples[name]
		if !ok {
			t.Errorf("no sample data for template %q", name)
			continue
		}
		var b bytes.Buffer
		if err := execute(&b, name, data); err != nil {
			t.Errorf("render %s: %v", name, err)
		}
	}
}
