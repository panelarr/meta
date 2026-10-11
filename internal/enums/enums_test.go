// SPDX-License-Identifier: GPL-3.0-or-later

package enums

import (
	"testing"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
)

func TestRoundTrip(t *testing.T) {
	if got := Name(metadatav1.EntryType_ENTRY_TYPE_LIGHT_NOVEL); got != "light_novel" {
		t.Fatalf("Name = %q", got)
	}
	if got := Name(metadatav1.EntryType_ENTRY_TYPE_UNSPECIFIED); got != "unspecified" {
		t.Fatalf("Name = %q", got)
	}
	v, ok := Parse[metadatav1.EntryType]("Light_Novel")
	if !ok || v != metadatav1.EntryType_ENTRY_TYPE_LIGHT_NOVEL {
		t.Fatalf("Parse = %v %v", v, ok)
	}
	if _, ok := Parse[metadatav1.EntryType]("comet"); ok {
		t.Fatal("unknown name parsed")
	}
	if MustParse[metadatav1.ContentRating]("erotic") != metadatav1.ContentRating_CONTENT_RATING_EROTIC {
		t.Fatal("MustParse")
	}
	if Name(metadatav1.EntryType(999)) != "unspecified" {
		t.Fatal("unknown number")
	}
}
