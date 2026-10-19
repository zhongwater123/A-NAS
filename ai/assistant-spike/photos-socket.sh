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
  pid=$(pgrep -u "$user" -f serve_nondumpable | head -1)
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
check "no thumbnails through the assistant socket" fail "not_found" -- inside alice python3 "$peer" "$bound" thumbnail "$alice_beach"
check "a photo's view, for the model to look at, is a JPEG" ok "image/jpeg" -- inside alice python3 "$peer" "$bound" view "$alice_beach"
check "and the view of bob's private photo is out of reach" fail "not_found" -- inside alice python3 "$peer" "$bound" view "$bob_beach"
check "no renaming through the assistant socket" fail "not_found" -- inside alice python3 "$peer" "$bound" rename "$alice_beach"
check "the photo service's own session socket is out of reach" unreachable -- inside alice python3 "$peer" /run/a-nas-photos/photos.sock --session seed-alice libraries
check "the assistant socket's real path is out of reach" unreachable -- inside alice python3 "$peer" "$hidden" libraries

echo "--- on the host, outside any sandbox"
check "a member's own process cannot reach the real path" unreachable -- host alice python3 "$peer" "$hidden" libraries
check "the sandbox path is only an empty file on the host" unreachable -- host alice python3 "$peer" "$bound" libraries
check "root at the other end is a wiring fault, not a member" fail "photos_unavailable" -- python3 "$peer" "$hidden" libraries
check "another member cannot reach it through alice's /proc" unreachable -- host bob python3 "$peer" "/proc/$(pgrep -u alice -f serve_nondumpable | head -1)/root$bound" libraries
# Not a boundary: a process of the same member reaches the sandbox's mount namespace through
# /proc/<pid>/root when ptrace is open to the same UID (Yama ptrace_scope 0, as on the
# Experimental NAS). It gets that member's own photos, read-only, as the assistant does.
pid=$(pgrep -u alice -f serve_nondumpable | head -1)
echo "INFO ptrace_scope $(cat /proc/sys/kernel/yama/ptrace_scope 2>/dev/null || echo unknown); alice's own process through /proc/$pid/root: $(host alice python3 "$peer" "/proc/$pid/root$bound" libraries 2>&1 | head -c 120)"

echo "--- the photo service restarts while an assistant runs"
# systemd holds the assistant socket, so the copy bound into the running sandbox is the
# same socket the restarted service answers on. A long turn keeps alice's assistant
# running throughout (the fake model sleeps). A service that has just started answers
# photos_unavailable until its store is open, as on its own socket.
"$py" /spike/serve_client.py alice chat "SLEEP 120" >/dev/null 2>&1 &
holder=$!
sleep 5
answered() { for _ in $(seq 1 30); do inside alice python3 "$peer" "$bound" libraries >/dev/null 2>&1 && return; sleep 1; done; }
pid=$(pgrep -u alice -f serve_nondumpable | head -1)
systemctl restart photos-spike
answered
check "alice's running assistant reaches the restarted service" ok "private:user:alice" -- inside alice python3 "$peer" "$bound" libraries
check "its photo tools still answer" ok "IMG_3101" -- inside alice "$py" -c \
  'import sys; sys.path.insert(0, "/spike"); import anas_mcp; print(anas_mcp.search_photos("海边").content[0].text)'
check "the assistant was not restarted for it" ok "^$pid\$" -- pgrep -u alice -f serve_nondumpable
systemctl stop photos-spike
inside alice python3 "$peer" "$bound" libraries >/dev/null 2>&1
check "a connection while the service is stopped starts it again" ok "^active" -- systemctl is-active photos-spike
answered
check "and it answers once its store is open" ok "private:user:alice" -- inside alice python3 "$peer" "$bound" libraries
# Only stopping the socket itself (uninstalling, upgrading the unit) leaves the sandbox
# with a dead socket; the photo tools then say so in words.
systemctl stop photos-spike-assistant.socket photos-spike.service
check "with the socket gone, the photo tools tell the model in words" ok "相册暂时连不上" -- inside alice "$py" -c \
  'import sys; sys.path.insert(0, "/spike"); import anas_mcp; print(anas_mcp.search_photos("海边").content[0].text)'
kill "$holder" 2>/dev/null
systemctl start photos-spike-assistant.socket photos-spike.service
systemctl stop anas-assistant@alice.service
"$py" /spike/serve_client.py alice ready >/dev/null
answered
check "a restarted assistant reaches the new socket" ok "private:user:alice" -- inside alice python3 "$peer" "$bound" libraries

echo "photos-socket: $pass passed, $fail failed"
((fail == 0))
