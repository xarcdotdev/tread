# Records docs/images/cli.gif: the real tread CLI driving scripts/demo, an agent on
# a fake Bluetooth backend with generated history. No treadmill or personal data.
#
# Run from the repository root (needs Go):
#   vhs scripts/cli.tape

Output docs/images/cli.gif

Require go

Set Shell bash
Set FontFamily "JetBrainsMono NFM SemiBold"
Set FontSize 18
Set Width 1000
Set Height 520
Set Padding 28
# Dashboard ink as background, its violet accent and status colors
Set Theme { "name": "tread", "background": "#12142a", "foreground": "#f4f5fa", "cursor": "#f4f5fa", "selection": "#2e3150", "black": "#12142a", "red": "#e5374f", "green": "#0fa968", "yellow": "#ffb020", "blue": "#00a8e8", "magenta": "#9e86ff", "cyan": "#00a8e8", "white": "#f4f5fa", "brightBlack": "#8b90a7", "brightRed": "#ff2d78", "brightGreen": "#0fa968", "brightYellow": "#ffb020", "brightBlue": "#00a8e8", "brightMagenta": "#b0a2ff", "brightCyan": "#00a8e8", "brightWhite": "#ffffff" }
Set TypingSpeed 60ms
Set WaitTimeout 3m

# Build both binaries, start the demo on its own port and wait for the fake link.
# mktemp keeps the agent socket path under the Unix limit.
Hide
Type `d=$(mktemp -d) && go build -o "$d/bin/" ./cmd/tread ./scripts/demo`
Enter
Wait
Type `"$d/bin/demo" -dir "$d" -listen 127.0.0.1:18799 2>"$d/demo.log" & demo=$!`
Enter
Type `export PATH="$d/bin:$PATH" TREAD_CONFIG="$d/config.toml" TREAD_STATE_DIR="$d/state" TREAD_RUNTIME_DIR="$d/run"`
Enter
Type `until tread observed 2>/dev/null | grep -q 'Connection: connected'; do sleep 0.1; done`
Enter
Wait
Type `PS1='\[\e[35m\]\$\[\e[0m\] '; clear`
Enter
Sleep 500ms
Show

Sleep 1s
Type "tread start"
Sleep 400ms
Enter
Sleep 1.5s
Type "tread speed 4"
Sleep 400ms
Enter
Sleep 1.5s
Type "tread speed-up"
Sleep 400ms
Enter
Sleep 1.5s
Type "tread stop"
Sleep 400ms
Enter
Sleep 1.5s
Type "tread stats -period 1m"
Sleep 400ms
Enter
Sleep 5s

Hide
Type `kill $demo; wait $demo; rm -rf "$d"`
Enter
Sleep 500ms
