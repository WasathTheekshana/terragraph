module "vpc" {
  source = "git::https://github.com/terraform-aws-modules/terraform-aws-vpc.git?ref=v5.1.0"
}

module "network_policies" {
  source = "git::ssh://git@github.com/example-org/tf-module-network-policies.git//modules/base?ref=v1.4.2"
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"
}

module "local_helpers" {
  source = "./modules/helpers"
}
