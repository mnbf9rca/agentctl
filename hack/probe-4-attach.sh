#!/bin/bash

SOCKET="agentctl-probe-4-$$"

tmux_cmd() {
  tmux -L "$SOCKET" "$@"
}

cleanup() {
  tmux_cmd kill-server >/dev/null 2>&1 || true
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

SESSION_ID=$(tmux_cmd new-session -d -s alpha -n w -P -F '#{session_id}' 'exec sleep 60')
echo "sid=$SESSION_ID"
echo -n "attach-session -t \$SESSION_ID     : "
tmux_cmd attach-session -t "$SESSION_ID" 2>&1 | head -1
echo -n "attach-session -t '=alpha'  : "
tmux_cmd attach-session -t '=alpha' 2>&1 | head -1
echo -n "attach-session -t '=nope'   : "
tmux_cmd attach-session -t '=nope' 2>&1 | head -1
echo -n "-CC attach-session -t \$SESSION_ID : "
tmux_cmd -CC attach-session -t "$SESSION_ID" 2>&1 | head -1
