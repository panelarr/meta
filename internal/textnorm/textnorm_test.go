// SPDX-License-Identifier: GPL-3.0-or-later

package textnorm

import "testing"

func TestKey(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":          "hello world",
		"  Pokémon   Adventure ": "pokemon adventure",
		"ＦＵＬＬ　ＷＩＤＴＨ":             "full width",
		"ｶﾀｶﾅ":                   "カタカナ",
		"Hell's Kitchen":         "hells kitchen",
		"Re:Zero − Starting":     "re zero starting",
		"進撃の巨人":                  "進撃の巨人",
		"Ánh Sáng":               "anh sang",
		"":                       "",
		"ガンダム":                   "ガンダム",
		"ﾊﾞｸﾏﾝ":                  "バクマン",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}
