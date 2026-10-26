#!/usr/bin/env bash
# await-ci.sh <commit> [minutes]: wait until go.yml has passed on <commit>,
# and fail if it failed there, or never ran.
#
# A release must not run ahead of CI. go.yml is the full suite — both
# toolchains, every example, servo check, lint — and release.yml's own
# checks are a fraction of it, so cut-release.yml waits for go.yml before
# it tags, and release.yml waits again before it builds, which covers a tag
# pushed by hand straight after its commit.
#
# Only runs of the commit itself count: a push, the nightly schedule, or a
# dispatch. A pull request's run tests a merge commit, under the pull
# request's own copy of the workflow. A commit with no run at all — one
# that arrived in the middle of a multi-commit push — can be given one with
# `gh workflow run go.yml --ref <its tag or branch>`.
#
# Needs GH_TOKEN (with actions: read) and GH_REPO in the environment.
set -euo pipefail

sha=$1
minutes=${2:-30}
started=$(date +%s)
deadline=$((started + minutes * 60))
# A run is created within seconds of its push, so a commit still without
# one after five minutes is not going to get one by waiting.
norun_deadline=$((started + 5 * 60))

while :; do
	runs=$(gh api "repos/$GH_REPO/actions/workflows/go.yml/runs?head_sha=$sha&per_page=100" \
		--jq '[.workflow_runs[] | select(.event == "push" or .event == "schedule" or .event == "workflow_dispatch")
		       | {status, conclusion, event, url: .html_url}]')
	passed=$(jq -r '[.[] | select(.conclusion == "success")][0].url // empty' <<< "$runs")
	if [ -n "$passed" ]; then
		echo "go.yml passed on $sha: $passed"
		exit 0
	fi
	total=$(jq 'length' <<< "$runs")
	pending=$(jq '[.[] | select(.status != "completed")] | length' <<< "$runs")
	now=$(date +%s)
	if [ "$total" -gt 0 ] && [ "$pending" -eq 0 ]; then
		echo "::error::go.yml did not pass on $sha. Fix it on master and release the commit that passes."
		jq -r '.[:10][] | "  \(.event): \(.conclusion) \(.url)"' <<< "$runs"
		exit 1
	fi
	if [ "$total" -eq 0 ]; then
		if [ "$now" -ge "$norun_deadline" ] || [ "$now" -ge "$deadline" ]; then
			echo "::error::go.yml has no run for $sha. Start one with: gh workflow run go.yml --ref <the tag or branch at $sha>, then try again."
			exit 1
		fi
	elif [ "$now" -ge "$deadline" ]; then
		echo "::error::go.yml is still running on $sha after $minutes minutes."
		jq -r '.[] | "  \(.event): \(.status) \(.url)"' <<< "$runs"
		exit 1
	fi
	echo "waiting for go.yml on $sha: $pending of $total run(s) in progress"
	sleep 30
done
