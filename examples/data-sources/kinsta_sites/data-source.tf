data "kinsta_sites" "all" {}

output "site_primary_domains" {
  value = { for s in data.kinsta_sites.all.sites : s.name => s.primary_domain }
}
