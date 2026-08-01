#!/bin/bash

SOCKET="agentctl-probe-3-$$"

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

echo "== A. session-ID targeting for set-option / show-options =="
SESSION_ID=$(tmux_cmd new-session -d -s alpha -n role1 -P -F '#{session_id}' 'exec sleep 300')
echo "created session id = '$SESSION_ID'"
tmux_cmd new-session -d -s alphabet -n w 'exec sleep 300'
echo -n "set-option -t \$SESSION_ID:  "
tmux_cmd set-option -t "$SESSION_ID" @agentctl_managed 1 2>&1 && echo OK
echo -n "show-options -qv -t \$SESSION_ID @agentctl_managed: '"
printf '%s' "$(tmux_cmd show-options -qv -t "$SESSION_ID" @agentctl_managed)"
echo "'"
echo -n "decoy 'alphabet' contaminated? managed='"
printf '%s' "$(tmux_cmd show-options -qv -t '$1' @agentctl_managed 2>&1)"
echo "'"
echo "-- proof bare-name is unsafe here: set-option -t alpha with alpha+alphabet present --"
tmux_cmd set-option -t alpha @probe x 2>&1 && echo "  -t alpha OK (resolved to: $(tmux_cmd show-options -qv -t "$SESSION_ID" @probe && echo alpha || echo other))"

echo
echo "== B. session-ID for the =-capable commands too =="
echo -n "list-windows -t \$SESSION_ID: "
tmux_cmd list-windows -t "$SESSION_ID" -F '#{session_name}' 2>&1
echo -n "has-session  -t \$SESSION_ID: "
tmux_cmd has-session -t "$SESSION_ID" 2>&1
echo "exit=$?"
echo -n "new-window   -t \$SESSION_ID: "
tmux_cmd new-window -d -t "$SESSION_ID" -n role2 -P -F '#{window_id} #{pane_id}' 'exec sleep 300' 2>&1
echo "kill-session -t \$SESSION_ID deferred; current sessions:"
tmux_cmd list-sessions -F '#{session_id}#{l:|}#{session_name}' 2>&1 | sed 's/^/  /'

echo
echo "== C. literal tab in -F, with arbitrary-value field last =="
WID=$(tmux_cmd list-windows -t "$SESSION_ID" -F '#{window_id} #{window_name}' | awk '$2=="role1"{print $1}')
tmux_cmd set-option -w -t "$WID" @agentctl_role role1
tmux_cmd set-option -w -t "$WID" @agentctl_process 'weird name'
tmux_cmd list-windows -t "$SESSION_ID" -F "#{window_id}$(printf '\t')#{window_name}$(printf '\t')#{@agentctl_role}$(printf '\t')#{@agentctl_process}" | sed -n 1p | od -c | head -4

echo
echo "== D. unset vs empty via format string =="
tmux_cmd set-option -w -t "$WID" @agentctl_model ''
echo -n "model set-to-empty via -F: '"
printf '%s' "$(tmux_cmd list-windows -t "$SESSION_ID" -F '#{@agentctl_model}' | sed -n 1p)"
echo "'"
echo -n "never-set option via -F:   '"
printf '%s' "$(tmux_cmd list-windows -t "$SESSION_ID" -F '#{@agentctl_never}' | sed -n 1p)"
echo "'"

echo
echo "== E. pane liveness fields =="
tmux_cmd new-window -d -t "$SESSION_ID" -n dying -P -F '#{pane_id}' 'exec sleep 300'
tmux_cmd list-panes -s -t "$SESSION_ID" -F '  #{window_name} pane=#{pane_id} pid=#{pane_pid} dead=#{pane_dead} panes_in_win=#{window_panes}' 2>&1

echo
echo "== F. does -P -F output land on stdout only? (stderr check) =="
stdout_value=$(tmux_cmd new-window -d -t "$SESSION_ID" -n probe2 -P -F '#{window_id}' 'exec sleep 300' 2>/dev/null)
echo "stdout='$stdout_value'"

echo
echo "== G. kill-session by ID =="
tmux_cmd kill-session -t "$SESSION_ID" 2>&1 && echo "killed by id OK"
echo "remaining:"
tmux_cmd list-sessions -F '  #{session_name}' 2>&1

echo
echo cleanup-done
