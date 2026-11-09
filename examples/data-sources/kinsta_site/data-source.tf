data "kinsta_site" "blog" {
  name = "blog" # site name or display name; or set id instead
}

output "blog_live_environment" {
  value = data.kinsta_site.blog.live_environment_id
}
