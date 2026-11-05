#!/usr/bin/env bash
set -euo pipefail
# Simulator-only ad-hoc identity; this is not an iPhone provisioning profile.
app=$1
entitlements=$(mktemp)
trap 'rm -f "$entitlements"' EXIT
cat > "$entitlements" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>application-identifier</key><string>com.anas.mobile</string>
  <key>keychain-access-groups</key><array><string>com.anas.mobile</string></array>
</dict></plist>
PLIST
if [ -d "$app/Frameworks" ]; then
  find "$app/Frameworks" -depth \( -name '*.framework' -o -name '*.dylib' \) \
    -exec codesign --force --sign - {} \;
fi
codesign --force --sign - --entitlements "$entitlements" --generate-entitlement-der "$app"
codesign --verify --strict "$app"
