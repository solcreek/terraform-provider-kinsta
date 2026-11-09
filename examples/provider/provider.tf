terraform {
  required_providers {
    kinsta = {
      source = "registry.opentofu.org/solcreek/kinsta"
    }
  }
}

provider "kinsta" {
  # The API key is read from the KINSTA_API_KEY environment variable by
  # default; it can also be set explicitly here.
  # api_key = "..."

  # Optional: the company is looked up from the API key when unset.
  # company_id = "..."
}
