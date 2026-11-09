# List region codes with the kinsta_regions data source.
resource "kinsta_site" "blog" {
  display_name   = "Blog"
  region         = "us-ashburn-1"
  admin_email    = "admin@example.com"
  admin_user     = "admin"
  admin_password = var.wordpress_admin_password
  site_title     = "My Blog"

  # Deleting a site cannot be undone. Set this to false and apply before
  # destroying the site.
  deletion_protection = true
}

# An empty site without WordPress, for content deployed by other means.
resource "kinsta_site" "headless" {
  display_name = "Headless"
  region       = "eu-frankfurt-1"
  install_mode = "plain"
}
