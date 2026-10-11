// SPDX-License-Identifier: GPL-3.0-or-later

import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { api, unwrap } from "../api/client";
import type { Client } from "../api/types";
import { CopyButton } from "../components/CopyButton";
import { ConfirmDialog, Dialog } from "../components/Dialog";
import { Badge, Empty, ErrorBox, Field, Loading, Mono, PageHeader } from "../components/ui";
import { formatDate } from "../lib/format";

export const scopes = [
  { scope: "catalog:read", help: "Browse, search and read the catalogue" },
  { scope: "admin", help: "Everything, including the admin API" },
] as const;

const clientsQuery = () =>
  queryOptions({
    queryKey: ["clients"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/clients"))).clients ?? [],
  });

type Shown = { client: Client; apiKey: string; rotated: boolean };

export function ClientsPage() {
  const clients = useQuery(clientsQuery());
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<Client | null>(null);
  const [rotating, setRotating] = useState<Client | null>(null);
  const [deleting, setDeleting] = useState<Client | null>(null);
  const [shown, setShown] = useState<Shown | null>(null);

  return (
    <>
      <PageHeader title="API clients">
        <button type="button" className="primary" onClick={() => setCreating(true)}>
          New client
        </button>
      </PageHeader>
      <p className="muted">
        Clients are callers such as a Panelarr server or a script. Each has its own API key and scopes. Keys are shown
        once, when created or rotated.
      </p>

      {clients.isPending ? <Loading /> : null}
      <ErrorBox error={clients.error} onRetry={() => clients.refetch()} />
      {clients.data?.length === 0 ? <Empty>No clients yet.</Empty> : null}
      {clients.data && clients.data.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Scopes</th>
              <th>Key prefix</th>
              <th>Created</th>
              <th>Rotated</th>
              <th>Last used</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {clients.data.map((client) => (
              <tr key={client.id}>
                <td>
                  {client.name}
                  <div>
                    <Mono>{client.id}</Mono>
                  </div>
                </td>
                <td>
                  <div className="badges">
                    {(client.scopes ?? []).map((scope) => (
                      <Badge key={scope} tone={scope === "admin" ? "warn" : "neutral"}>
                        {scope}
                      </Badge>
                    ))}
                  </div>
                </td>
                <td>
                  <Mono>{client.keyPrefix}…</Mono>
                </td>
                <td>{formatDate(client.createdAt)}</td>
                <td>{formatDate(client.rotatedAt)}</td>
                <td>{client.lastUsedAt ? formatDate(client.lastUsedAt) : <span className="muted">never</span>}</td>
                <td className="row-actions">
                  <button type="button" className="small" onClick={() => setEditing(client)}>
                    Edit
                  </button>
                  <button type="button" className="small" onClick={() => setRotating(client)}>
                    Rotate key
                  </button>
                  <button type="button" className="small danger" onClick={() => setDeleting(client)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      {creating ? (
        <ClientDialog
          onClose={() => setCreating(false)}
          onCreated={(client, apiKey) => {
            setCreating(false);
            setShown({ client, apiKey, rotated: false });
          }}
        />
      ) : null}
      {editing ? <ClientDialog client={editing} onClose={() => setEditing(null)} /> : null}
      {rotating ? (
        <RotateDialog
          client={rotating}
          onClose={() => setRotating(null)}
          onRotated={(client, apiKey) => {
            setRotating(null);
            setShown({ client, apiKey, rotated: true });
          }}
        />
      ) : null}
      {deleting ? <DeleteDialog client={deleting} onClose={() => setDeleting(null)} /> : null}
      {shown ? <KeyDialog shown={shown} onClose={() => setShown(null)} /> : null}
    </>
  );
}

function ClientDialog({
  client,
  onClose,
  onCreated,
}: {
  client?: Client;
  onClose: () => void;
  onCreated?: (client: Client, apiKey: string) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(client?.name ?? "");
  const [selected, setSelected] = useState<string[]>(client?.scopes ?? ["catalog:read"]);

  const save = useMutation({
    mutationFn: async () => {
      if (selected.length === 0) throw new Error("Pick at least one scope.");
      const body = { name: name.trim(), scopes: selected };
      if (client) {
        await unwrap(
          api.PATCH("/v1/admin/clients/{clientId}", { params: { path: { clientId: client.id ?? "" } }, body }),
        );
        return null;
      }
      return unwrap(api.POST("/v1/admin/clients", { body }));
    },
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ["clients"] });
      if (created?.client && created.apiKey) onCreated?.(created.client, created.apiKey);
      else onClose();
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate();
  };

  const toggle = (scope: string, on: boolean) =>
    setSelected((prev) => scopes.map((s) => s.scope).filter((s) => (s === scope ? on : prev.includes(s))));

  return (
    <Dialog title={client ? `Edit ${client.name}` : "New client"} onClose={onClose}>
      <form className="form-grid" onSubmit={submit}>
        <Field label="Name">
          <input type="text" required value={name} onChange={(event) => setName(event.target.value)} />
        </Field>
        <fieldset className="field">
          <legend className="field-label">Scopes</legend>
          {scopes.map(({ scope, help }) => (
            <label key={scope} className="filter-check">
              <input
                type="checkbox"
                checked={selected.includes(scope)}
                onChange={(event) => toggle(scope, event.target.checked)}
              />
              <span>
                <Mono>{scope}</Mono> <span className="muted small">{help}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <ErrorBox error={save.error} />
        <div className="actions">
          <button type="submit" className="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : client ? "Save" : "Create"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function RotateDialog({
  client,
  onClose,
  onRotated,
}: {
  client: Client;
  onClose: () => void;
  onRotated: (client: Client, apiKey: string) => void;
}) {
  const queryClient = useQueryClient();
  const rotate = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/v1/admin/clients/{clientId}/rotate", { params: { path: { clientId: client.id ?? "" } } })),
    onSuccess: async (response) => {
      await queryClient.invalidateQueries({ queryKey: ["clients"] });
      onRotated(response.client ?? client, response.apiKey ?? "");
    },
  });
  return (
    <ConfirmDialog
      title={`Rotate the key of ${client.name}?`}
      confirmLabel="Rotate key"
      pending={rotate.isPending}
      error={rotate.error}
      onConfirm={() => rotate.mutate()}
      onClose={onClose}
    >
      <p>The current key stops working at once.</p>
    </ConfirmDialog>
  );
}

function DeleteDialog({ client, onClose }: { client: Client; onClose: () => void }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/v1/admin/clients/{clientId}", { params: { path: { clientId: client.id ?? "" } } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["clients"] });
      onClose();
    },
  });
  return (
    <ConfirmDialog
      title={`Delete ${client.name}?`}
      confirmLabel="Delete client"
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate()}
      onClose={onClose}
    >
      <p>Its key stops working at once.</p>
    </ConfirmDialog>
  );
}

function KeyDialog({ shown, onClose }: { shown: Shown; onClose: () => void }) {
  return (
    <Dialog
      title={shown.rotated ? `New key for ${shown.client.name}` : `Key for ${shown.client.name}`}
      onClose={onClose}
    >
      <p className="notice">
        <strong>Copy this key now.</strong> It won't be shown again: only a hash is stored. If you lose it, rotate the
        key.
      </p>
      <div className="key-box">
        <code className="mono" data-testid="api-key">
          {shown.apiKey}
        </code>
      </div>
      <div className="actions">
        <CopyButton value={shown.apiKey} label="Copy key" />
        <button type="button" className="primary" onClick={onClose}>
          I've copied it
        </button>
      </div>
    </Dialog>
  );
}
