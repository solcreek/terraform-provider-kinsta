#!/bin/sh
# SPDX-License-Identifier: MPL-2.0
# Runs the provider tests through a given CLI: terraform or tofu.
set -eu
cli="${1:-terraform}"
case "$cli" in
  terraform|tofu) ;;
  *) echo "Usage: $0 [terraform|tofu]" >&2; exit 2 ;;
esac
TF_ACC_TERRAFORM_PATH="$(command -v "$cli")"
export TF_ACC_TERRAFORM_PATH
go test -race -count=1 -timeout=15m ./internal/provider
