#!/bin/bash
# greenboot required check: the local control plane must be healthy
# this boot. Probes the apiserver + node + control-plane static pods
# via `nanokube healthcheck`, which decouples cluster-health judgement
# from the nanokube.service exit code (a transient bookkeeping failure
# inside the supervisor must not flip a working cluster into rollback).
#
# Greenboot retries this script across boot_counter iterations before
# tripping the rollback path, so a brief startup race is tolerated.
set -eu

if ! systemctl is-active --quiet nanokube.service ; then
    echo "nanokube.service is not active" >&2
    systemctl status --no-pager nanokube.service >&2 || true
    exit 1
fi

if ! nanokube healthcheck --timeout=5m ; then
    echo "nanokube healthcheck failed" >&2
    exit 1
fi
