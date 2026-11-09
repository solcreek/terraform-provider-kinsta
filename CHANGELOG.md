# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `kinsta_site` resource: create a WordPress site (`install_mode = "new"`) or an
  empty one (`"plain"`) with its live environment. `deletion_protection`
  defaults to `true`; installation arguments are creation-only. Import by site ID.
- `kinsta_environment` resource: add a staging environment (standard or
  premium) to a site. Refuses to delete a live environment; `deletion_protection`
  defaults to `true`. Import by `<site_id>/<environment_id>`.
- `kinsta_site_domain` resource: add a domain to an environment and export the
  verification and pointing records Kinsta asks for. Import by
  `<environment_id>/<domain_name>`.
- `kinsta_primary_domain` resource: set an environment's primary domain, with
  optional search-and-replace. Import by `<site_id>/<environment_id>`.
- `kinsta_dns_record` resource: manage a record set on Kinsta DNS. Accepts `@`,
  relative or fully qualified names, and bare or quoted TXT values. Import by
  `<zone_id>/<type>/<fqdn>`.
- `kinsta_site`, `kinsta_sites`, `kinsta_dns_zone`, `kinsta_regions` and
  `kinsta_site_domain` data sources.
- Dependency-free Kinsta API v2 client: API-key auth, company lookup from the
  key, operation polling with Kinsta's failure reasons, serialized requests
  with rate-limit back-off, and actionable credential errors.

[unreleased]: https://github.com/solcreek/terraform-provider-kinsta/commits/main
