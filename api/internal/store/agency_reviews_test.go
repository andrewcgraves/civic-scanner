package store

import (
	"strings"
	"testing"
)

func TestValidLiveSlug(t *testing.T) {
	valid := []string{"la-metro", "octa", "a1", "big-bear-transit", "2nd-district", strings.Repeat("a", MaxSlugLength), "submitted", "submittedx"}
	for _, slug := range valid {
		if !ValidLiveSlug(slug) {
			t.Errorf("ValidLiveSlug(%q) = false, want true", slug)
		}
	}
	invalid := []string{
		"", "a", strings.Repeat("a", MaxSlugLength+1), "La-Metro", "la_metro", "la metro", "-metro", "metro-", "la--metro",
		"submitted-", "submitted-3f2a", "métro", "la.metro", "la/metro", "feed.xml", " metro",
	}
	for _, slug := range invalid {
		if ValidLiveSlug(slug) {
			t.Errorf("ValidLiveSlug(%q) = true, want false", slug)
		}
	}
}
