# Changelog

## 0.1.0 (2026-11-09)


### Features

* **admin:** health page and the metrics scope ([565f302](https://github.com/panelarr/meta/commit/565f302d5d3ee446ac12253be10af01ab5059f02))
* **admin:** show and remove pushed profiles; DNS over HTTPS for local profiles ([441df45](https://github.com/panelarr/meta/commit/441df45843d92aa81c69d6a07fb2479b1b35ba41))
* **admin:** web admin UI served at / ([7e7f4f2](https://github.com/panelarr/meta/commit/7e7f4f281a37f1538a4268793cdb8b4ab2e6e902))
* **api:** browse, search, entry details and external lookups ([2331852](https://github.com/panelarr/meta/commit/2331852126556ff3aac344ececaa1a546958eb69))
* **auth:** admin token and scoped client API keys ([93d8574](https://github.com/panelarr/meta/commit/93d85743a923217f72d8edc687d96dd37b4567e2))
* **build:** image, compose example and release automation ([fca9551](https://github.com/panelarr/meta/commit/fca955115f068c8590ad12f31e6d04cd012a1c39))
* **duplicates:** find entries that are the same work, for a curator to merge or dismiss ([324acd6](https://github.com/panelarr/meta/commit/324acd6d6bbc1f31f2121c70db7fd523fe7cb9fd))
* **ingest:** plugin host, sync runner, scheduler and merged entries ([5852fee](https://github.com/panelarr/meta/commit/5852fee5a22693407706e925eef31a6fdda4f796))
* **integrations:** read back pushed profiles; DNS over HTTPS in profiles ([1f4a246](https://github.com/panelarr/meta/commit/1f4a246243684b31722243ca8edb77f6df4b4475))
* **integrations:** solvers, network profiles and per-provider assignments ([0ff3e7a](https://github.com/panelarr/meta/commit/0ff3e7aa70c973c7eb0e71d393f0dc6309ad9535))
* **jobs:** jobs API and admin Jobs page ([743e687](https://github.com/panelarr/meta/commit/743e687af4c35fff268d0f366519a3eac5121117))
* **jobs:** say when a job can't run now ([f8e976b](https://github.com/panelarr/meta/commit/f8e976b2901ecea86ff4fa74e2fe31faf11e8605))
* **observability:** Prometheus metrics and a detailed health view ([b2aa7f4](https://github.com/panelarr/meta/commit/b2aa7f445a92f69439010b51127e750b0ce3bb98))
* **proto:** metadata plugin protocol, meta API and the Go plugin SDK ([d9e0aed](https://github.com/panelarr/meta/commit/d9e0aed673229a9eb559051a8bcb5495e789f12f))
* **sdk:** call remote plugins over gRPC by default ([d70264e](https://github.com/panelarr/meta/commit/d70264e2ecc16906edbd8c18ab558db54c28007b))
* **sdk:** keep the order of settings fields ([805d263](https://github.com/panelarr/meta/commit/805d263cc41da93707774c813243a34bb24ce7bb))
* **sdk:** network profiles reach plugins and their HTTP clients ([8832b4a](https://github.com/panelarr/meta/commit/8832b4a240f2dc594f69399e9216bdbae1f7c33c))
* **search:** optional Typesense and Meilisearch behind the search interface ([43e7adf](https://github.com/panelarr/meta/commit/43e7adf466a9b287d0271d567e49d7d9fc866eab))
* **store:** PostgreSQL data layer for the entry schema ([e49bbce](https://github.com/panelarr/meta/commit/e49bbce81632e0b26d0dd4b57b5a82f810edd4a6))


### Bug Fixes

* **api:** say which provider's plugin is down ([5117089](https://github.com/panelarr/meta/commit/511708923b99843d3d3498fce81c5198262e6ff7))
* **catalog:** tag identity by kind and path, tag filters and Panelarr branding ([1b08ac9](https://github.com/panelarr/meta/commit/1b08ac9e0ec7561bb3a822ee7ca418391e4311b4))
* **ci:** bump Go to 1.27.3 and klauspost/compress to 1.20.2; skip CodeQL on private repos ([e2cc5b0](https://github.com/panelarr/meta/commit/e2cc5b08fd27f2a9f026cb39e957e688606da375))
* **ci:** bump Go to 1.27.3 and klauspost/compress to 1.20.2; skip CodeQL on private repos ([3c44c7a](https://github.com/panelarr/meta/commit/3c44c7ad93067327c74357e73adb8d45a6e9405d))
* **ci:** run buf breaking against the local checkout ([136df16](https://github.com/panelarr/meta/commit/136df16bb18557b8d2a672d426141afe5db6d092))
* **ingest:** a full database disk fails the sync with a clear message ([543732e](https://github.com/panelarr/meta/commit/543732e74589f978eb0c5f77e1597480bf944268))
* **ingest:** refuse a sync when the provider's plugin has stopped ([1787dcf](https://github.com/panelarr/meta/commit/1787dcf9282493a00fdda4710da875338b888f01))
