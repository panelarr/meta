// SPDX-License-Identifier: Apache-2.0

// Command static-plugin is a minimal metadata plugin that serves a few
// made-up records. It shows how a plugin is put together with the SDK, and
// the SDK's tests run it in managed and standalone mode.
//
//	go run ./sdk/examples/static-plugin --listen :8903
package main

import (
	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/sdk/plugin"
	"github.com/panelarr/meta/sdk/plugintest"
)

func main() {
	p := &plugintest.Fake{Provider: "static", SnapshotID: "static-1"}
	p.Entries = []*metadatav1.ProviderEntry{
		p.Entry("1", "The Lantern Keeper", "Toudai Mori"),
		p.Entry("2", "Salt and Iron"),
		p.Entry("3", "A Quiet Orbit", "Shizuka na Kidou"),
	}
	p.Merges = []plugintest.Merge{{Merged: "4", Into: "2"}}
	plugin.Main(p, plugin.WithDefaultListen(":8903"))
}
