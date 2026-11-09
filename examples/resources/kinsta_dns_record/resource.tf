data "kinsta_dns_zone" "example" {
  name = "example.com"
}

resource "kinsta_dns_record" "apex" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = "@"
  type    = "A"
  values  = ["192.0.2.10"]
}

resource "kinsta_dns_record" "www" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = "www"
  type    = "CNAME"
  values  = ["example.com"]
}

resource "kinsta_dns_record" "mx" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = "@"
  type    = "MX"
  values  = ["1 aspmx.l.google.com", "5 alt1.aspmx.l.google.com"]
}

# TXT values may be written bare; they are quoted the way Kinsta DNS stores them.
resource "kinsta_dns_record" "spf" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = "@"
  type    = "TXT"
  values  = ["v=spf1 include:_spf.google.com -all"]
  ttl     = 3600
}
