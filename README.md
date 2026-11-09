<a href="https://terraform.io">
    <img src=".github/tf.svg" alt="Terraform logo" title="Terraform" align="left" height="50" />
</a>
<a href="https://opentofu.org">
    <picture>
        <source media="(prefers-color-scheme: dark)" srcset=".github/opentofu-dark.svg" />
        <img src=".github/opentofu-light.svg" alt="OpenTofu logo" title="OpenTofu" align="left" height="50" />
    </picture>
</a>

# Terraform / OpenTofu Provider for Kinsta

[![GitHub tag (latest SemVer)](https://img.shields.io/github/v/tag/solcreek/terraform-provider-kinsta?label=release&style=for-the-badge)](https://github.com/solcreek/terraform-provider-kinsta/releases/latest) [![License](https://img.shields.io/github/license/solcreek/terraform-provider-kinsta.svg?style=for-the-badge)](LICENSE) [![Tests](https://img.shields.io/github/actions/workflow/status/solcreek/terraform-provider-kinsta/test.yml?branch=main&style=for-the-badge)](https://github.com/solcreek/terraform-provider-kinsta/actions)

A provider for [Kinsta](https://kinsta.com) WordPress hosting: sites, staging
environments, site domains, primary domains and records on Kinsta DNS. It uses a
**dependency-free, standard-library-only** client for the
[Kinsta API v2](https://api-docs.kinsta.com), so the provider owns its HTTP
behaviour: it waits for Kinsta's asynchronous operations to finish, backs off when
rate limited, and turns credential errors into actionable messages.

> [!NOTE]
> This is an **unofficial, community-maintained** provider. It is **not**
> affiliated with, endorsed by, or supported by Kinsta Inc. "Kinsta" is a
> trademark of its respective owner and is used here only to describe what the
> provider integrates with.

Proudly built for the open-source IaC ecosystem and dedicated to **OpenTofu**.

## Usage

> [!IMPORTANT]
> The provider is published on the **[OpenTofu Registry](https://search.opentofu.org/provider/solcreek/kinsta)**
> only, so always use the **full** `registry.opentofu.org/solcreek/kinsta`
> address. It works with both OpenTofu and Terraform. The short
> `solcreek/kinsta` form only works with OpenTofu: Terraform resolves short
> addresses against `registry.terraform.io`.

```hcl
terraform {
  required_providers {
    kinsta = {
      source = "registry.opentofu.org/solcreek/kinsta"
    }
  }
}

provider "kinsta" {
  # api_key  = "..."   # or the KINSTA_API_KEY environment variable
  # company_id = "..." # optional; looked up from the API key when unset
}

data "kinsta_site" "blog" {
  name = "blog"
}

# Add a domain and publish the records Kinsta asks for on Kinsta DNS.
resource "kinsta_site_domain" "www" {
  environment_id = data.kinsta_site.blog.live_environment_id
  domain_name    = "www.example.com"
}

data "kinsta_dns_zone" "example" {
  name = "example.com"
}

resource "kinsta_dns_record" "www" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = "www"
  type    = "CNAME"
  values  = ["example.com"]
}
```

See [`examples/`](./examples) for every resource and data source.

## Resources & data sources

| Kind | Name | Purpose |
|------|------|---------|
| resource | `kinsta_site` | Create a WordPress site (or an empty one) with its live environment. Deletion protected by default. |
| resource | `kinsta_environment` | Add a staging environment to a site. Deletion protected by default. |
| resource | `kinsta_site_domain` | Add a domain to an environment; exports the verification and pointing records Kinsta asks for. |
| resource | `kinsta_primary_domain` | Set an environment's primary domain (optionally with search-and-replace). |
| resource | `kinsta_dns_record` | Manage one record set on Kinsta DNS. |
| data | `kinsta_site` | Look up a site by ID, name or display name. |
| data | `kinsta_sites` | List every site of the company. |
| data | `kinsta_dns_zone` | Look up a Kinsta DNS zone by name. |
| data | `kinsta_regions` | List the regions sites can be created in. |
| data | `kinsta_site_domain` | Read a domain of an environment and the records it still needs. |

All resources support `terraform import`.

## Authentication

Create an API key in MyKinsta under *Company settings → API keys* and supply it
with the `api_key` argument or the `KINSTA_API_KEY` environment variable. A key
belongs to one company: unless `company_id` (or `KINSTA_COMPANY_ID`) is set, the
provider asks the API which one (`GET /validate`) when it is configured.

What happens with bad credentials:

- **No key** → the provider fails at configuration time with
  *"Missing Kinsta credentials"*.
- **Invalid, expired or revoked key** → the API returns `401`, surfaced as such.
- **Insufficient access level** → the API returns `403`, surfaced as a hint that
  the key lacks the access this call needs.

## Behaviour worth knowing

- **Operations are waited for.** Kinsta answers most writes with `202` and an
  operation ID. The provider polls each operation to completion and reports
  Kinsta's own reason when one fails, so `apply` only succeeds once the change has
  landed. Requests are sent one at a time, since Kinsta throttles bursts of
  parallel calls; throttled requests are retried with back-off.
- **Deleting is guarded.** `kinsta_site` and `kinsta_environment` default to
  `deletion_protection = true`: a delete removes files and databases and Kinsta
  has no undo. A change that would replace them (such as renaming a site, which
  Kinsta has no API for) is blocked the same way. A delete counts as done only
  once the object is gone, not when its operation reports success.
- **Creation-only arguments.** The WordPress installation arguments (`region`,
  admin user, title, language, Multisite, WooCommerce, Yoast) are only sent when a
  site or environment is created. Kinsta never returns them, so they are not
  compared with the live installation, changing one later replaces nothing (you
  get a warning instead), and imported sites do not need them.
- **Primary domains.** An environment always has a primary domain, so destroying
  `kinsta_primary_domain` leaves it as it is. Kinsta refuses to remove the primary
  domain: move it elsewhere before destroying that `kinsta_site_domain`.
- **Domains are added without a wildcard.** Add `www.` and other hosts as
  `kinsta_site_domain` resources of their own.
- **DNS record names** can be `@`, relative (`www`) or fully qualified
  (`www.example.com`). Spellings of the same name are equal and never replace the
  record. Kinsta stores names with a trailing dot; `fqdn` reports them without.
- **TXT values** may be written bare (`v=spf1 -all`). Kinsta DNS stores them in
  zone-file form, quoted and split into 255-character strings
  (`"v=spf1 -all"`); the provider quotes them on write and treats both spellings
  as equal.
- **Regions** use Kinsta's current codes such as `us-ashburn-1` or
  `eu-frankfurt-1`; list them with `data.kinsta_regions`.

## Limitations

- **Focused surface.** Sites, staging environments, domains and Kinsta DNS
  records only. Backups, caching, PHP settings, redirects, SFTP and Kinsta's
  application and database hosting are not managed.
- **No rename.** The API cannot rename a site or environment.
- **Secrets in state.** `admin_password` is stored in state (marked sensitive),
  like every Terraform argument. Change it in WordPress after creation if your
  state is shared.
- **What is verified live** (2026-11-09): every resource's import and read and
  every data source; and the full `kinsta_dns_record` lifecycle (create, update
  values and TTL, import, delete) through the acceptance test. Site,
  environment and domain writes follow the API reference and are exercised
  against an in-memory fake of the API, not yet against the live API.

## Development

```sh
make build          # compile
make test           # unit + lifecycle tests against an in-memory Kinsta API (needs terraform on PATH)
make test-tofu      # the same tests through OpenTofu
make testacc        # acceptance tests — needs KINSTA_API_KEY and KINSTA_TEST_ZONE
make lint           # golangci-lint
make generate       # regenerate docs/ from schema + examples + templates
```

The unit tests need **no credentials**: each resource's whole lifecycle (create,
plan, update, import, drift, destroy) runs against an in-memory fake of the API
that mirrors the live API's observed quirks. Acceptance tests (`TF_ACC`) make
real API calls; they create and delete a TXT record with a random name in the
zone `KINSTA_TEST_ZONE` names, so point it at a zone you may write to.

To run a local build, use a CLI dev override (OpenTofu reads the same file
format as `~/.tofurc`):

```hcl
# ~/.terraformrc  (or set TF_CLI_CONFIG_FILE)
provider_installation {
  dev_overrides { "registry.opentofu.org/solcreek/kinsta" = "/abs/path/to/dir/with/binary" }
  direct {}
}
```

## Releasing

Move the `Unreleased` entries in `CHANGELOG.md` under the new version, then push
a `v*` tag (e.g. `git tag v0.1.0 && git push origin v0.1.0`). The Release
workflow runs the tests, checks that the signing key is the registered Solcreek
identity, and runs GoReleaser, which builds every platform, signs the
`SHA256SUMS` file and publishes a GitHub release with that version's changelog
section as its notes.

No registry PR is needed per version once the provider is registered: new
versions appear on the OpenTofu Registry automatically. Check with:

```sh
curl -s https://registry.opentofu.org/v1/providers/solcreek/kinsta/versions
```

Keep in mind:

- **Treat a pushed tag as final.** The registry indexes every semver tag and
  never changes a version's artifacts
  ([policy](https://github.com/opentofu/registry/blob/main/POLICY.md#version-immutability)).
  To fix a bad release, release a new version. A `release-tags` ruleset forbids
  moving or deleting `v*` tags, and immutable releases forbid replacing
  published assets.
- **Do not change the signing key.** `tofu init` verifies `SHA256SUMS.sig`
  against the key registered in
  [opentofu/registry](https://github.com/opentofu/registry/tree/main/keys/s/solcreek).
- **Keep the asset names.** `.goreleaser.yml` produces the names the registry
  protocol expects.

These repository settings (plus the description and topics shown on
search.opentofu.org) are declared in `scripts/repo-settings.sh`. Run it to
check for drift, and with `--apply` (admin rights) to reconcile.

## License

[Mozilla Public License 2.0](LICENSE) — the license used by OpenTofu's core
community providers.
