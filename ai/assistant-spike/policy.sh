# shellcheck shell=bash
# Sourced by the test scripts that change the managed Hermes policy for a run (#114).
# `policy YAML` starts from the original /usr/lib/a-nas/assistant/managed/config.yaml, merges YAML into it key by key
# (a block it already has, like skills:, keeps its other keys) and stops alice's instance, so the
# next request starts it under that policy. The original comes back, and the instance stops
# again, when the script exits for any reason.
policy_py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
policy_orig=/tmp/managed-config.yaml.orig

policy_restore() {
  if [[ -f $policy_orig ]]; then
    cp -p "$policy_orig" /usr/lib/a-nas/assistant/managed/config.yaml && rm -f "$policy_orig"
  fi
  systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
  return 0
}
trap policy_restore EXIT

policy() {
  [[ -f $policy_orig ]] || cp -p /usr/lib/a-nas/assistant/managed/config.yaml "$policy_orig"
  cp -p "$policy_orig" /usr/lib/a-nas/assistant/managed/config.yaml
  if [[ -n ${1:-} ]]; then
    "$policy_py" - "$1" <<'PY'
import json
import sys
from ruamel.yaml import YAML
yaml = YAML(typ="safe")
def merge(base, extra):
    for key, value in extra.items():
        base[key] = merge(base.get(key) or {}, value) if isinstance(value, dict) else value
    return base
policy = merge(yaml.load(open("/usr/lib/a-nas/assistant/managed/config.yaml")), yaml.load(sys.argv[1]))
# JSON, which is YAML too: dumped as YAML a string like "off" comes out bare, and Hermes,
# reading YAML 1.1, takes it for false.
with open("/usr/lib/a-nas/assistant/managed/config.yaml", "w") as out:
    json.dump(policy, out, ensure_ascii=False, indent=1)
PY
    echo "managed policy for this run adds: $(tr '\n' ' ' <<<"$1")"
  fi
  systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
  return 0
}
