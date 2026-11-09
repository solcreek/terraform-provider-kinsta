resource "kinsta_primary_domain" "blog" {
  site_id        = kinsta_site.blog.id
  environment_id = kinsta_site.blog.live_environment_id
  domain_name    = kinsta_site_domain.apex.domain_name

  # Rewrite the old URL to the new one in the WordPress database.
  run_search_and_replace = true
}
