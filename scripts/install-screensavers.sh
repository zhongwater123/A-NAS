#!/usr/bin/env bash
# Kept for one version under its old name, which runbooks and the experimental
# NAS still use: install-media-pool.sh screensavers does the same.
#
#   install-screensavers.sh POOL_ID [STAGING] | --import DIRECTORY
exec "$(dirname -- "$0")/install-media-pool.sh" screensavers "$@"
