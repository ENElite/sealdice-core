#!/bin/sh
set -eu

# Service code and credentials stay in the optional persistent /codex mount.
if [ -f /codex/mcp-imagegen/start-http.sh ]; then
    /bin/bash /codex/mcp-imagegen/start-http.sh &
fi

exec "$@"
