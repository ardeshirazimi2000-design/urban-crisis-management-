#!/usr/bin/env bash
# Operational installation (AFTA security profile): built-in sign-in with Shahkar + SMS OTP, HTTPS only.
#   sudo APIIR_TOKEN='<api.ir key>' APIIR_SHAHKAR_ENDPOINT=Shahkar bash deploy/install-production.sh
# Re-running updates the code and keeps secrets and data. See docs/deploy-production.md.
set -euo pipefail
MODE=production exec bash "$(dirname "$0")/install-lan.sh" "$@"
