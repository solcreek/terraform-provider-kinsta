data "kinsta_site" "blog" {
  name = "blog"
}

data "kinsta_site_domain" "apex" {
  environment_id = data.kinsta_site.blog.live_environment_id
  domain_name    = "example.com"
}

output "records_still_needed" {
  value = concat(
    data.kinsta_site_domain.apex.verification_records,
    data.kinsta_site_domain.apex.pointing_records,
  )
}
