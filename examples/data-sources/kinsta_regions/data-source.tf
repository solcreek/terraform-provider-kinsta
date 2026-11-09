data "kinsta_regions" "all" {}

output "region_codes" {
  value = data.kinsta_regions.all.regions[*].region
}
