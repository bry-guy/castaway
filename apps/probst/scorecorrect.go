package main

import (
	"context"
	"fmt"
	"net/url"
)

// findOccurrence is the ID of the instance's occurrence with exactly this name, e.g. "Free the Block: Episode 6".
func findOccurrence(ctx context.Context, call apiCall, instancePath, name string) (string, error) {
	var acts struct {
		Activities []struct {
			ID string `json:"id"`
		} `json:"activities"`
	}
	if err := call(ctx, "GET", instancePath+"/activities", nil, &acts); err != nil {
		return "", err
	}
	for _, a := range acts.Activities {
		var occ struct {
			Occurrences []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"occurrences"`
		}
		if err := call(ctx, "GET", "/activities/"+url.PathEscape(a.ID)+"/occurrences", nil, &occ); err != nil {
			return "", err
		}
		for _, o := range occ.Occurrences {
			if o.Name == name {
				return o.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no %q found; is the game resolved?", name)
}
