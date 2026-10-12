# App catalog provenance

The `docker-compose.yml` and `icon.*` files in this directory are copied without modification from [IceWhaleTech/CasaOS-AppStore](https://github.com/IceWhaleTech/CasaOS-AppStore) at commit `0909364b800950030e71ea82355a5969a1c08b39` (2026-09-24), which is licensed under the Apache License 2.0 (see [LICENSE](LICENSE)). Directory names are lower-cased upstream app directory names.

A-NAS does not run these files as-is: `internal/appstore` validates each manifest against its install policy and renders a rewritten Compose file with A-NAS host paths and labels. App names, logos and container images belong to their respective projects and are subject to their own licenses and trademarks.

The catalog is a reviewed selection of the apps at the pinned commit. Apps the policy rejects (host devices, host networking, ports below 1024 or host paths outside `/DATA`, among others) are never included; which of the remaining apps are included, and why others are not, is recorded in the [app catalog review](../../../docs/research/app-catalog-review-2026-10.md). A-NAS lists each app under its own category, assigned in [`categories.go`](../categories.go), so these files stay unmodified.
