#!/bin/bash

SOCKET="agentctl-probe-2-$$"

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

tmux_cmd new-session -d -s alpha -n role1 'exec sleep 300'

echo "== A. set-option / show-options session targeting =="
echo -n "set-option -t '=alpha':  "
tmux_cmd set-option -t '=alpha' @k v 2>&1 || echo "  <- FAILS"
echo -n "set-option -t alpha:     "
tmux_cmd set-option -t alpha @k v 2>&1 && echo OK
echo -n "show-options -qv -t alpha @k: '"
printf '%s' "$(tmux_cmd show-options -qv -t alpha @k 2>&1)"
echo "'"
echo -n "show-options -qv -t '=alpha' @k: '"
printf '%s' "$(tmux_cmd show-options -qv -t '=alpha' @k 2>&1)"
echo "'"

echo
echo "== B. is bare set/show session lookup EXACT? (only 'alpha' exists) =="
echo -n "set-option -t alph @k2 v: "
tmux_cmd set-option -t alph @k2 v 2>&1 && echo "OK <- PREFIX MATCHED (bad)" || echo "  <- refused (exact)"
echo -n "show-options -qv -t alph @k: '"
printf '%s' "$(tmux_cmd show-options -qv -t alph @k 2>&1)"
echo "'"

echo
echo "== C. session prefix matching for =-capable commands (unique prefix) =="
tmux_cmd new-session -d -s betabeta -n w 'exec sleep 300'
echo -n "has-session -t betab (unique prefix): "
tmux_cmd has-session -t betab 2>&1
echo "exit=$? (0 = prefix matched)"
echo -n "has-session -t '=betab':             "
tmux_cmd has-session -t '=betab' 2>&1
echo "exit=$?"
echo -n "list-windows -t betab:               "
tmux_cmd list-windows -t betab -F '#{session_name}' 2>&1
echo -n "kill-session -t betab would hit:     "
tmux_cmd display-message -p -t betab '#{session_name}' 2>&1

echo
echo "== D. window-name prefix matching (only 'reviewer' exists) =="
tmux_cmd new-window -d -t '=alpha' -n reviewer 'exec sleep 300'
echo -n "list-panes -t 'alpha:rev'  -> "
tmux_cmd list-panes -t 'alpha:rev' -F '#{window_name}' 2>&1
echo -n "list-panes -t 'alpha:=rev' -> "
tmux_cmd list-panes -t 'alpha:=rev' -F '#{window_name}' 2>&1
echo "   exit=$?"
echo -n "list-panes -t 'alpha:REVIEWER' -> "
tmux_cmd list-panes -t 'alpha:REVIEWER' -F '#{window_name}' 2>&1

echo
echo "== E. duplicate window names =="
tmux_cmd new-window -d -t '=alpha' -n dup 'exec sleep 300'
tmux_cmd new-window -d -t '=alpha' -n dup 'exec sleep 300'
echo "windows named dup:"
tmux_cmd list-windows -t '=alpha' -F '  #{window_id} #{window_name}' | grep dup
echo -n "list-panes -t 'alpha:dup' picks: "
tmux_cmd list-panes -t 'alpha:dup' -F '#{window_id}' 2>&1

echo
echo "== F. show-options -v on window ID vs unset, and exit codes =="
WID=$(tmux_cmd list-windows -t '=alpha' -F '#{window_id} #{window_name}' | awk '$2=="reviewer"{print $1}')
tmux_cmd set-option -w -t "$WID" @agentctl_managed 1
option_value=$(tmux_cmd show-options -wqv -t "$WID" @agentctl_managed)
echo "set:   '$option_value' exit=$?"
option_value=$(tmux_cmd show-options -wqv -t "$WID" @agentctl_absent)
echo "unset: '$option_value' exit=$?"
option_value=$(tmux_cmd show-options -wv -t "$WID" @agentctl_absent 2>&1)
echo "unset no -q: '$option_value' exit=$?"

echo
echo "== G. list-windows -F with tab separator + user options =="
tmux_cmd list-windows -t '=alpha' -F '#{window_id}#{l:	}#{window_name}#{l:	}#{@agentctl_managed}' 2>&1 | od -c | head -5

echo
echo "== H. dead pane / remain-on-exit state =="
tmux_cmd new-window -d -t '=alpha' -n shortlived 'exec true'
sleep 1
echo -n "window still present? "
tmux_cmd list-windows -t '=alpha' -F '#{window_name}' | grep -c shortlived

echo
echo "== I. display-message from inside a pane context =="
PANE_ID=$(tmux_cmd list-panes -t 'alpha:=reviewer' -F '#{pane_id}')
echo -n "display-message -p -t \$PANE_ID '#{session_name}': "
tmux_cmd display-message -p -t "$PANE_ID" '#{session_name}' 2>&1

echo
echo "== J. new-session -P -F to get IDs back at creation =="
tmux_cmd new-session -d -s gamma -n g1 -P -F '#{session_id} #{window_id} #{pane_id}' 'exec sleep 300' 2>&1
tmux_cmd new-window -d -t '=gamma' -n g2 -P -F '#{window_id} #{pane_id}' 'exec sleep 300' 2>&1

echo
echo cleanup-done
