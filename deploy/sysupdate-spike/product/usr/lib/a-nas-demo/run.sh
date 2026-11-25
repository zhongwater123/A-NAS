#!/bin/sh
# The stand-in product service: records which version runs, then idles.
set -eu
echo "@VERSION@" > /var/lib/a-nas-demo/running-version
exec sleep infinity
