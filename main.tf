terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = "demo-dataops-project"
  region  = "us-central1"
}

# locals {
#   # List of Data Engineers who need access
#   data_engineers = ["user:alice.smith@company.com", "user:bob.jones@company.com", "user:sharaon.abishek@company.com", "user:abishek89@company.com", "user:david.kim@company.com", "user:hijk.lmnop@company.com"]
# }

module "bigquery_viewer_data_engineering" {
  source   = "git::https://bitbucket.org/company/tf-modules//iam-access"
  for_each = toset(["user:alice.smith@company.com", "user:bob.jones@company.com", "user:abc.xyz@company.com"])

  role   = "roles/bigquery.dataViewer"
  member = each.value
}


module "bigquery_jobuser_data_engineering" {
  source  = "git::https://bitbucket.org/company/tf-modules//iam-access"
  role    = "roles/bigquery.jobuser"
  members = ["user:david.kim@company.com"]
}


module "bigquery_dataEditor_supply_chain" {
  source  = "git::https://bitbucket.org/company/tf-modules//iam-access"
  role    = "roles/bigquery.dataEditor"
  members = ["user:abc.xyz@company.com"]
}



module "bigquery_viewer_supply_chain" {
  source  = "git::https://bitbucket.org/company/tf-modules//iam-access"
  role    = "roles/bigquery.viewer"
  members = ["user:abc.xyz@company.com"]
}

