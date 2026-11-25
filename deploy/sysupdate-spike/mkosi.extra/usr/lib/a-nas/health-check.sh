#!/bin/sh
# The boot health check of the verification image. build-info comes from
# mkosi.postinst; the health-broken variant fails on purpose.
set -eu
# shellcheck source=/dev/null
. /usr/lib/a-nas/build-info
if [ "$ANAS_VARIANT" = health-broken ]; then
	echo "a-nas health: version $ANAS_VERSION fails on purpose" >&2
	exit 1
fi
mountpoint -q /srv/a-nas
test -d /var/lib/a-nas
echo "a-nas health: version $ANAS_VERSION is healthy"
