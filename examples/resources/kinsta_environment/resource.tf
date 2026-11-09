resource "kinsta_environment" "staging" {
  site_id      = kinsta_site.blog.id
  display_name = "Staging"
  install_mode = "plain"
}
