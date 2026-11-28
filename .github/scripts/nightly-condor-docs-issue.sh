#!/usr/bin/env bash
#
# Report a failed nightly HTCondor docs staging run as a GitHub issue.
#
# Deduplication follows nightly-fuzz-issue.sh: one open issue carries the
# label named by DOCS_LABEL ("nightly-condor-docs"). If such an issue is
# open this comments on the most recently created one, otherwise it opens
# a new one, so a failure that persists night after night grows one
# thread instead of a pile of issues.
#
# When the failure clears nothing happens: the issue stays open and a
# human closes it. Closing it while the failure persists means the next
# run opens a fresh one.
#
# Every body carries the marker as an HTML comment, so these are findable
# by body search even if the label is removed.
#
# Environment:
#   GH_TOKEN         token with issues: write
#   GH_REPO          owner/name
#   HTCONDOR_REF     the ref the docs were staged from
#   HTCONDOR_SHA     the commit that ref resolved to
#   HTCONDOR_LATEST  upstream's latest release tag, for context
#   RUN_URL          link to the workflow run
#   DOCS_LOG         file holding the staging/test output
#   DOCS_LABEL       dedup label (default: nightly-condor-docs)
set -euo pipefail

: "${GH_TOKEN:?set GH_TOKEN}"
: "${GH_REPO:?set GH_REPO}"

label="${DOCS_LABEL:-nightly-condor-docs}"
marker="<!-- nightly-condor-docs -->"
ref="${HTCONDOR_REF:-main}"
sha="${HTCONDOR_SHA:-unknown}"
latest="${HTCONDOR_LATEST:-unknown}"
run_url="${RUN_URL:-unknown}"
log="${DOCS_LOG:-/dev/null}"

body="$(mktemp)"
trap 'rm -f "$body"' EXIT

{
	echo "$marker"
	echo
	echo "Staging HTCondor's RST docs from \`${ref}\` failed, so the MCP doc tools"
	echo "would ship without the pages they serve."
	echo
	echo "| | |"
	echo "| --- | --- |"
	echo "| Staged from | \`${ref}\` at \`${sha}\` |"
	echo "| Upstream latest release | \`${latest}\` |"
	echo "| Run | ${run_url} |"
	echo
	echo "**The likely cause is an upstream path move.** \`make stage-condor-docs\`"
	echo "copies named files (\`classad-attributes/job-classad-attributes.rst\`,"
	echo "\`classad-attributes/machine-classad-attributes.rst\`,"
	echo "\`man-pages/condor_submit.rst\`) and globs"
	echo "\`admin-manual/configuration/*.rst\`. A renamed file fails the copy; a moved"
	echo "directory empties the glob and fails the TestEmbedded_* assertions instead."
	echo
	echo "This is caught here rather than in a pull request on purpose: CI and the"
	echo "release both stage from upstream HEAD, so without this the breakage lands"
	echo "on whoever pushes next, on an unrelated change."
	echo
	echo "<details><summary>Output</summary>"
	echo
	echo '```'
	tail -n 120 "$log" 2>/dev/null || echo "(no log captured)"
	echo '```'
	echo
	echo "</details>"
} > "$body"

# The label has to exist before --label will accept it, and a workflow
# that depends on somebody having clicked something once is a workflow
# that fails the first time it is needed. Idempotent: an existing label
# makes this a no-op.
gh label create "$label" \
	--description "Nightly HTCondor docs staging failed" \
	--color B60205 >/dev/null 2>&1 || true

# Same query shape as nightly-fuzz-issue.sh, so the two dedup the same
# way rather than two ways.
existing="$(gh issue list --state open --label "$label" \
	--limit 1 --json number --jq '.[0].number // empty')"

if [ -n "$existing" ]; then
	echo "commenting on existing issue #$existing"
	gh issue comment "$existing" --body-file "$body"
else
	echo "opening a new issue"
	gh issue create \
		--title "Nightly: staging HTCondor docs from $ref failed" \
		--label "$label" \
		--body-file "$body"
fi
