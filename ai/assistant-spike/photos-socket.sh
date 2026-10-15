#!/usr/bin/env bash
# The photo service's assistant socket (#113): who can reach it from where, and what each
# member's assistant sees through it. Runs as root in a container started with SPIKE_PHOTOS=1.
set -uo pipefail
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
peer=/spike/photos_peer.py
bound=/run/anas-assistant-photos/photos.sock
hidden=/run/a-nas-photos-assistant/assistant.sock
pass=0 fail=0
# check NAME ok|fail|unreachable [GREP] -- CMD...: the command succeeds, answers non-2xx, or
# cannot connect at all; with GREP, its output must match it (a leading ! means must not).
check() {
  local name=$1 want=$2 pattern="" out code
  shift 2
  [[ $1 != -- ]] && { pattern=$1; shift; }
  shift
  out=$("$@" 2>&1); code=$?
  local good=0
  case $want in
    ok) ((code == 0)) && good=1 ;;
    fail) ((code == 1)) && good=1 ;;
    unreachable) ((code != 0 && code != 1)) && good=1 ;;
  esac
  if ((good)) && [[ -n $pattern ]]; then
    if [[ $pattern == !* ]]; then grep -qE -- "${pattern:1}" <<<"$out" && good=0; else grep -qE -- "$pattern" <<<"$out" || good=0; fi
  fi
  if ((good)); then echo "PASS $name: ${out:0:160}"; pass=$((pass + 1)); else echo "FAIL $name: ${out:0:300}"; fail=$((fail + 1)); fi
}
# inside USER CMD...: as USER, in the mount namespace of that member's running assistant.
inside() {
  local user=$1 pid
  shift
  pid=$(pgrep -u "$user" -f 'hermes serve' | head -1)
  nsenter -t "$pid" -m -- setpriv --reuid "$user" --regid "$user" --groups 20000 -- "$@"
}
host() { local user=$1; shift; setpriv --reuid "$user" --regid "$user" --groups 20000 -- "$@"; }

for user in alice bob; do "$py" /spike/serve_client.py "$user" ready >/dev/null; done
# The member's photo IDs, as the photo service's own session socket gives them.
bob_beach=$(python3 "$peer" /run/a-nas-photos/photos.sock --session seed-bob ids 海边 | grep -o 'IMG_9001.jpg=[^,'"'"' ]*' | cut -d= -f2)
alice_beach=$(python3 "$peer" /run/a-nas-photos/photos.sock --session seed-alice ids 海边 | grep -o 'IMG_3101.jpg=[^,'"'"' ]*' | cut -d= -f2)
shared_beach=$(python3 "$peer" /run/a-nas-photos/photos.sock --session seed-alice ids 海边 | grep -o 'IMG_7001.jpg=[^,'"'"' ]*' | cut -d= -f2)
echo "INFO photo IDs: alice ${alice_beach:-?}, bob ${bob_beach:-?}, shared ${shared_beach:-?}"

echo "--- inside each member's assistant sandbox"
check "alice's assistant: own private and the shared library" ok "private:user:alice.*shared" -- inside alice python3 "$peer" "$bound" libraries
check "bob's assistant: own private and the shared library" ok "private:user:bob.*shared" -- inside bob python3 "$peer" "$bound" libraries
check "alice's search: own and shared beach photos, not bob's" ok "IMG_3101.*IMG_7001" -- inside alice python3 "$peer" "$bound" search 海边
check "alice's search never lists bob's private photos" ok '!IMG_900' -- inside alice python3 "$peer" "$bound" search 海边
check "bob's search: own and shared, not alice's" ok '!IMG_31' -- inside bob python3 "$peer" "$bound" search 海边
check "alice's assistant opens a shared photo" ok "IMG_7001" -- inside alice python3 "$peer" "$bound" get "$shared_beach"
check "alice's assistant cannot open bob's private photo" fail "not_found" -- inside alice python3 "$peer" "$bound" get "$bob_beach"
check "a session token sent along changes nothing" fail "not_found" -- inside alice python3 "$peer" "$bound" --session seed-bob get "$bob_beach"
check "no thumbnails through the assistant socket" fail -- inside alice python3 "$peer" "$bound" thumbnail "$alice_beach"
check "no renaming through the assistant socket" fail -- inside alice python3 "$peer" "$bound" rename "$alice_beach"
check "the photo service's own session socket is out of reach" unreachable -- inside alice python3 "$peer" /run/a-nas-photos/photos.sock --session seed-alice libraries
check "the assistant socket's real path is out of reach" unreachable -- inside alice python3 "$peer" "$hidden" libraries

echo "--- on the host, outside any sandbox"
check "a member's own process cannot reach the real path" unreachable -- host alice python3 "$peer" "$hidden" libraries
check "the sandbox path is only an empty file on the host" unreachable -- host alice python3 "$peer" "$bound" libraries
check "a UID that is no member's gets nothing" fail "authentication_required" -- python3 "$peer" "$hidden" libraries

echo "--- the photo service restarts while an assistant runs"
# A long turn keeps alice's assistant running across the restart (the fake model sleeps).
"$py" /spike/serve_client.py alice chat "SLEEP 120" >/dev/null 2>&1 &
holder=$!
sleep 5
systemctl restart photos-spike
for _ in $(seq 1 60); do [[ -S $hidden ]] && python3 "$peer" "$hidden" libraries 2>/dev/null | grep -q '^401' && break; sleep 1; done
check "the running sandbox still holds the old socket" unreachable "Connection refused" -- inside alice python3 "$peer" "$bound" libraries
kill "$holder" 2>/dev/null
systemctl stop anas-assistant@alice.service
"$py" /spike/serve_client.py alice ready >/dev/null
check "a restarted assistant reaches the new one" ok "private:user:alice" -- inside alice python3 "$peer" "$bound" libraries

echo "photos-socket: $pass passed, $fail failed"
((fail == 0))
