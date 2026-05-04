#!/bin/bash
# greenboot wanted check: advisory surface of the most recent nanokube
# lifecycle event. Non-failing by design — the message shows up in
# MOTD and `journalctl -u greenboot-healthcheck.service`.
set -u

EVENT_FILE=/var/lib/nanokube/state/last-event

if [ -s "$EVENT_FILE" ] ; then
    echo "nanokube: $(cat "$EVENT_FILE")"
else
    echo "nanokube: no events recorded"
fi
