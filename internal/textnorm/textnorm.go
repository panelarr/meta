// SPDX-License-Identifier: GPL-3.0-or-later

// Package textnorm normalizes names and titles for matching and search:
// compatibility forms folded (full-width Latin, half-width kana), diacritics
// removed, lower case, punctuation turned into spaces, spaces collapsed.
// The same function runs when meta stores text and when it searches, so the
// database needs no extensions beyond pg_trgm.
package textnorm

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// diacritics are the combining marks used by Latin, Greek and Cyrillic
// scripts. Marks of other scripts (such as the kana voicing marks) change
// the letter and are kept.
var diacritics = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0300, Hi: 0x036f, Stride: 1},
		{Lo: 0x1ab0, Hi: 0x1aff, Stride: 1},
		{Lo: 0x1dc0, Hi: 0x1dff, Stride: 1},
		{Lo: 0x20d0, Hi: 0x20ff, Stride: 1},
		{Lo: 0xfe20, Hi: 0xfe2f, Stride: 1},
	},
}

// Key returns the normalized form of s.
func Key(s string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(diacritics)), norm.NFKC)
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = norm.NFKC.String(s)
	}
	var b strings.Builder
	b.Grow(len(folded))
	space := false
	for _, r := range strings.ToLower(folded) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// "Hell's" and "Hells" should match.
		default:
			space = true
		}
	}
	return b.String()
}
