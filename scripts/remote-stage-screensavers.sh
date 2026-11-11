#!/usr/bin/env bash
# Kept for one version under its old name: remote-stage-media-pool.sh
# screensavers does the same.
#
#   remote-stage-screensavers.sh missing|commit HASH...
exec bash "$(dirname -- "$0")/remote-stage-media-pool.sh" screensavers "$@"
