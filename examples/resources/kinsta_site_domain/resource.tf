resource "kinsta_site_domain" "apex" {
  environment_id = kinsta_site.blog.live_environment_id
  domain_name    = "example.com"
}

resource "kinsta_site_domain" "www" {
  environment_id = kinsta_site.blog.live_environment_id
  domain_name    = "www.example.com"
}

# Publish the records Kinsta asks for with any DNS provider, for example
# Kinsta DNS itself. They are known once the domain has been added.
locals {
  apex_records = concat(
    kinsta_site_domain.apex.verification_records,
    kinsta_site_domain.apex.pointing_records,
  )
}

output "dns_records_for_example_com" {
  value = local.apex_records
}
