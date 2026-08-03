#!/bin/bash

SOCKET="agentctl-probe-1-$$"
KEY_CAPTURE="/tmp/agentctl-probe-keys-$$.txt"

tmux_cmd() {
  tmux -L "$SOCKET" "$@"
}

cleanup() {
  tmux_cmd kill-server >/dev/null 2>&1 || true
  rm -f "$KEY_CAPTURE"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

echo "== tmux version =="
tmux_cmd -V

echo
echo "== 1. new-session with -- before shell-command =="
tmux_cmd new-session -d -s probe -n role1 -c /tmp -- 'exec sleep 300' 2>&1 && echo "OK exit=$?" || echo "FAIL exit=$?"
tmux_cmd list-windows -t '=probe' -F 'win=#{window_id} name=#{window_name}' 2>&1
tmux_cmd list-panes -t '=probe' -F 'pane=#{pane_id} pid=#{pane_pid} dead=#{pane_dead} cmd=#{pane_current_command}' 2>&1
echo "-- pane cwd check --"
tmux_cmd list-panes -t '=probe' -F '#{pane_current_path}' 2>&1

echo
echo "== 2. new-window with -- =="
tmux_cmd new-window -d -t '=probe' -n role2 -c /tmp -- 'exec sleep 300' 2>&1 && echo OK || echo FAIL
tmux_cmd list-windows -t '=probe' -F '#{window_id} #{window_name}' 2>&1

echo
echo "== 3. session user options =="
tmux_cmd set-option -t '=probe' @agentctl_managed 1 2>&1 && echo "set OK" || echo "set FAIL"
echo -n "read set:   "
tmux_cmd show-options -qv -t '=probe' @agentctl_managed 2>&1
echo "  exit=$?"
echo -n "read unset: '"
unset_value=$(tmux_cmd show-options -qv -t '=probe' @agentctl_nope 2>&1)
unset_status=$?
printf '%s' "$unset_value"
echo "' exit=$unset_status"
echo -n "read unset no -q: '"
unset_value=$(tmux_cmd show-options -v -t '=probe' @agentctl_nope 2>&1)
unset_status=$?
printf '%s' "$unset_value"
echo "' exit=$unset_status"

echo
echo "== 4. window user options via window ID =="
WID=$(tmux_cmd list-windows -t '=probe' -F '#{window_id} #{window_name}' | awk '$2=="role1"{print $1}')
echo "role1 window id = $WID"
tmux_cmd set-option -w -t "$WID" @agentctl_role role1 2>&1 && echo "set OK" || echo "set FAIL"
tmux_cmd set-option -w -t "$WID" @agentctl_process '2.1.220' 2>&1
tmux_cmd set-option -w -t "$WID" @agentctl_model '' 2>&1 && echo "empty-value set OK" || echo "empty-value set FAIL"
echo -n "read role:  "
tmux_cmd show-options -wqv -t "$WID" @agentctl_role 2>&1
echo -n "read model(empty): '"
printf '%s' "$(tmux_cmd show-options -wqv -t "$WID" @agentctl_model 2>&1)"
echo "'"
echo "-- value containing a space --"
tmux_cmd set-option -w -t "$WID" @agentctl_spacey 'two words' 2>&1
echo -n "  -v read:   '"
printf '%s' "$(tmux_cmd show-options -wqv -t "$WID" @agentctl_spacey)"
echo "'"
echo "  no -v read: $(tmux_cmd show-options -w -t "$WID" @agentctl_spacey)"

echo
echo "== 5. user options in -F format strings =="
tmux_cmd list-windows -t '=probe' -F 'id=#{window_id} name=#{window_name} role=#{@agentctl_role} proc=#{@agentctl_process}' 2>&1

echo
echo "== 6. '=' exact match vs prefix match =="
tmux_cmd new-session -d -s foo -n w 'exec sleep 300' 2>&1
tmux_cmd new-session -d -s foobar -n w 'exec sleep 300' 2>&1
echo -n "has-session -t foo (no =): "
tmux_cmd has-session -t foo 2>&1 && echo exit0
echo -n "has-session -t '=foo':     "
tmux_cmd has-session -t '=foo' 2>&1 && echo exit0
tmux_cmd kill-session -t '=foo' 2>&1
echo -n "after killing =foo, has-session -t '=foobar': "
tmux_cmd has-session -t '=foobar' 2>&1 && echo "exit0 (foobar survived)"
echo -n "has-session -t '=foo' now: "
tmux_cmd has-session -t '=foo' 2>&1
echo "exit=$?"
echo "-- does bare -t foo prefix-match foobar? --"
tmux_cmd new-session -d -s foobar2 -n w 'exec sleep 300' 2>&1
echo -n "has-session -t fooba: "
tmux_cmd has-session -t fooba 2>&1
echo "exit=$? (0 would prove prefix matching)"

echo
echo "== 7. window name prefix matching danger =="
tmux_cmd new-window -d -t '=probe' -n rev 'exec sleep 300' 2>&1
tmux_cmd new-window -d -t '=probe' -n reviewer 'exec sleep 300' 2>&1
echo -n "list-panes -t 'probe:rev' resolves to: "
tmux_cmd list-panes -t 'probe:rev' -F '#{window_name}' 2>&1
echo -n "list-panes -t 'probe:=rev' resolves to: "
tmux_cmd list-panes -t 'probe:=rev' -F '#{window_name}' 2>&1

echo
echo "== 8. display-message session name =="
tmux_cmd display-message -p -t '=probe' '#{session_name}' 2>&1

echo
echo "== 9. send-keys argv shape (to a cat pane) =="
tmux_cmd new-window -d -t '=probe' -n keys -- "exec cat > $KEY_CAPTURE" 2>&1
PANE_ID=$(tmux_cmd list-panes -t 'probe:=keys' -F '#{pane_id}')
echo "pane=$PANE_ID"
tmux_cmd send-keys -t "$PANE_ID" -l -- '/clear' 2>&1 && echo "literal send OK" || echo "literal send FAIL"
tmux_cmd send-keys -t "$PANE_ID" Enter 2>&1 && echo "Enter OK" || echo "Enter FAIL"
sleep 1
echo -n "captured: '"
printf '%s' "$(sed -n '1p' "$KEY_CAPTURE" 2>/dev/null)"
echo "'"

echo
echo "== cleanup =="
echo 'done'
