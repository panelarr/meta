// SPDX-License-Identifier: GPL-3.0-or-later

// Short names for the generated API types (src/api/schema.d.ts).
import type { components } from "./schema";

type Schemas = components["schemas"];

// panelarr.meta.v1: the catalogue
export type EntrySummary = Schemas["panelarr.meta.v1.EntrySummary"];
export type Entry = Schemas["panelarr.meta.v1.Entry"];
export type EntrySource = Schemas["panelarr.meta.v1.EntrySource"];
export type ProviderStats = Schemas["panelarr.meta.v1.ProviderStats"];
export type BrowseResponse = Schemas["panelarr.meta.v1.BrowseResponse"];
export type SearchResponse = Schemas["panelarr.meta.v1.SearchResponse"];
export type SearchHit = Schemas["panelarr.meta.v1.SearchHit"];
export type GetEntryResponse = Schemas["panelarr.meta.v1.GetEntryResponse"];
export type SortField = Schemas["panelarr.meta.v1.SortField"];

// panelarr.meta.v1: administration
export type Provider = Schemas["panelarr.meta.v1.Provider"];
export type SyncRun = Schemas["panelarr.meta.v1.SyncRun"];
export type SyncRunStatus = Schemas["panelarr.meta.v1.SyncRunStatus"];
export type SyncRunNotice = Schemas["panelarr.meta.v1.SyncRunNotice"];
export type WhoAmI = Schemas["panelarr.meta.v1.WhoAmIResponse"];
export type Client = Schemas["panelarr.meta.v1.Client"];
export type Solver = Schemas["panelarr.meta.v1.Solver"];
export type SolverType = Schemas["panelarr.meta.v1.SolverType"];
export type NetworkProfile = Schemas["panelarr.meta.v1.NetworkProfile"];
export type UserAgentMode = Schemas["panelarr.meta.v1.UserAgentMode"];
export type Assignment = Schemas["panelarr.meta.v1.Assignment"];
export type ProfileOrigin = Schemas["panelarr.meta.v1.ProfileOrigin"];

// panelarr.metadata.v1: the entry schema and plugins
export type EntryType = Schemas["panelarr.metadata.v1.EntryType"];
export type EntryStatus = Schemas["panelarr.metadata.v1.EntryStatus"];
export type ContentRating = Schemas["panelarr.metadata.v1.ContentRating"];
export type Tag = Schemas["panelarr.metadata.v1.Tag"];
export type Title = Schemas["panelarr.metadata.v1.Title"];
export type Image = Schemas["panelarr.metadata.v1.Image"];
export type Attribution = Schemas["panelarr.metadata.v1.Attribution"];
export type ExternalId = Schemas["panelarr.metadata.v1.ExternalId"];
export type PluginInfo = Schemas["panelarr.metadata.v1.PluginInfo"];
export type HealthStatus = Schemas["panelarr.metadata.v1.HealthStatus"];
export type PartialDate = Schemas["panelarr.metadata.v1.PartialDate"];
