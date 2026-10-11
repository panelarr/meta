// SPDX-License-Identifier: GPL-3.0-or-later

package catalog

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	metadatav1 "github.com/panelarr/meta/gen/panelarr/metadata/v1"
	"github.com/panelarr/meta/internal/enums"
	"github.com/panelarr/meta/internal/store/db"
	"github.com/panelarr/meta/internal/textnorm"
)

func (t *tx) lookupKnown(key string) (uuid.UUID, bool) {
	if id, ok := t.known[key]; ok {
		return id, true
	}
	t.w.mu.RLock()
	defer t.w.mu.RUnlock()
	id, ok := t.w.known[key]
	return id, ok
}

func idLists(xs []*metadatav1.ExternalId) (providers, ids []string) {
	for _, x := range xs {
		if x.GetProvider() != "" && x.GetId() != "" {
			providers = append(providers, x.GetProvider())
			ids = append(ids, x.GetId())
		}
	}
	return providers, ids
}

func (t *tx) person(ctx context.Context, p *metadatav1.Person) (uuid.UUID, error) {
	providers, ids := idLists(p.GetExternalIds())
	nameKey := textnorm.Key(p.GetName())
	data := proto.Clone(p).(*metadatav1.Person)
	data.Id, data.Name = "", ""
	js, err := pj.Marshal(data)
	if err != nil {
		return uuid.Nil, err
	}
	if len(ids) > 0 {
		key := "person:" + providers[0] + ":" + ids[0]
		if id, ok := t.lookupKnown(key); ok {
			return id, nil
		}
		id, err := t.q.FindPersonByExternalIDs(ctx, db.FindPersonByExternalIDsParams{Providers: providers, ExternalIds: ids})
		switch {
		case err == nil:
		case errors.Is(err, pgx.ErrNoRows):
			id = uuid.Must(uuid.NewV7())
			if err := t.q.InsertPerson(ctx, db.InsertPersonParams{ID: id, Name: p.GetName(), NameKey: nameKey, HasIds: true, Data: js}); err != nil {
				return uuid.Nil, err
			}
		default:
			return uuid.Nil, err
		}
		for i := range ids {
			if err := t.q.InsertPersonExternalID(ctx, db.InsertPersonExternalIDParams{Provider: providers[i], ExternalID: ids[i], PersonID: id}); err != nil {
				return uuid.Nil, err
			}
		}
		t.known[key] = id
		return id, nil
	}
	key := "person-name:" + nameKey
	if id, ok := t.lookupKnown(key); ok {
		return id, nil
	}
	id, err := t.q.UpsertNameOnlyPerson(ctx, db.UpsertNameOnlyPersonParams{ID: uuid.Must(uuid.NewV7()), Name: p.GetName(), NameKey: nameKey, Data: js})
	if err != nil {
		return uuid.Nil, err
	}
	t.known[key] = id
	return id, nil
}

func (t *tx) company(ctx context.Context, c *metadatav1.Company) (uuid.UUID, error) {
	providers, ids := idLists(c.GetExternalIds())
	nameKey := textnorm.Key(c.GetName())
	data := proto.Clone(c).(*metadatav1.Company)
	data.Id, data.Name = "", ""
	js, err := pj.Marshal(data)
	if err != nil {
		return uuid.Nil, err
	}
	if len(ids) > 0 {
		key := "company:" + providers[0] + ":" + ids[0]
		if id, ok := t.lookupKnown(key); ok {
			return id, nil
		}
		id, err := t.q.FindCompanyByExternalIDs(ctx, db.FindCompanyByExternalIDsParams{Providers: providers, ExternalIds: ids})
		switch {
		case err == nil:
		case errors.Is(err, pgx.ErrNoRows):
			id = uuid.Must(uuid.NewV7())
			if err := t.q.InsertCompany(ctx, db.InsertCompanyParams{ID: id, Name: c.GetName(), NameKey: nameKey, HasIds: true, Data: js}); err != nil {
				return uuid.Nil, err
			}
		default:
			return uuid.Nil, err
		}
		for i := range ids {
			if err := t.q.InsertCompanyExternalID(ctx, db.InsertCompanyExternalIDParams{Provider: providers[i], ExternalID: ids[i], CompanyID: id}); err != nil {
				return uuid.Nil, err
			}
		}
		t.known[key] = id
		return id, nil
	}
	key := "company-name:" + nameKey
	if id, ok := t.lookupKnown(key); ok {
		return id, nil
	}
	id, err := t.q.UpsertNameOnlyCompany(ctx, db.UpsertNameOnlyCompanyParams{ID: uuid.Must(uuid.NewV7()), Name: c.GetName(), NameKey: nameKey, Data: js})
	if err != nil {
		return uuid.Nil, err
	}
	t.known[key] = id
	return id, nil
}

// tag returns the shared tag for a provider's tag, creating or updating it.
// Tags are shared by normalized name. A tag's own data (description,
// flags) is written the first time this Writer meets it, not for every
// entry that carries it; a restart picks up changes.
func (t *tx) tag(ctx context.Context, tg *metadatav1.Tag) (uuid.UUID, error) {
	nameKey := textnorm.Key(tg.GetName())
	key := "tag:" + nameKey
	if id, ok := t.lookupKnown(key); ok {
		return id, nil
	}
	data := proto.Clone(tg).(*metadatav1.Tag)
	data.Id, data.Name, data.Description, data.Parent, data.Level = "", "", "", "", 0
	data.IsGenre, data.IsSpoiler, data.IsExplicit, data.ContentRating, data.EntryCount = false, false, false, 0, 0
	js, err := pj.Marshal(data)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := t.q.UpsertTag(ctx, db.UpsertTagParams{
		ID: uuid.Must(uuid.NewV7()), Name: tg.GetName(), NameKey: nameKey, Description: tg.GetDescription(),
		ParentKey: textnorm.Key(tg.GetParent()), Level: tg.GetLevel(), IsGenre: tg.GetIsGenre(),
		IsSpoiler: tg.GetIsSpoiler(), IsExplicit: tg.GetIsExplicit(), ContentRating: enums.Name(tg.GetContentRating()),
		Data: js,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = t.q.GetTagIDByKey(ctx, nameKey)
	}
	if err != nil {
		return uuid.Nil, err
	}
	for _, x := range tg.GetExternalIds() {
		if x.GetProvider() == "" || x.GetId() == "" {
			continue
		}
		if err := t.q.InsertTagExternalID(ctx, db.InsertTagExternalIDParams{Provider: x.GetProvider(), ExternalID: x.GetId(), TagID: id}); err != nil {
			return uuid.Nil, err
		}
	}
	t.known[key] = id
	return id, nil
}

// prefetchPersons creates or finds, in one statement, the name-only people
// that the entries credit and that this Writer hasn't met yet.
func (t *tx) prefetchPersons(ctx context.Context, ids []uuid.UUID, merged map[uuid.UUID]*Merged) error {
	var params db.UpsertNameOnlyPersonsParams
	seen := map[string]bool{}
	for _, id := range ids {
		for _, c := range merged[id].Entry.GetCredits() {
			p := c.GetPerson()
			if p.GetName() == "" || len(p.GetExternalIds()) > 0 || !onlyName(p) {
				continue
			}
			key := textnorm.Key(p.GetName())
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, ok := t.lookupKnown("person-name:" + key); ok {
				continue
			}
			params.Ids = append(params.Ids, uuid.Must(uuid.NewV7()))
			params.Names = append(params.Names, p.GetName())
			params.NameKeys = append(params.NameKeys, key)
		}
	}
	if len(params.Ids) == 0 {
		return nil
	}
	rows, err := t.q.UpsertNameOnlyPersons(ctx, params)
	if err != nil {
		return err
	}
	for _, r := range rows {
		t.known["person-name:"+r.NameKey] = r.ID
	}
	return nil
}

// onlyName reports whether a person carries nothing but a name.
func onlyName(p *metadatav1.Person) bool {
	c := proto.Clone(p).(*metadatav1.Person)
	c.Name, c.Id = "", ""
	return proto.Size(c) == 0
}
