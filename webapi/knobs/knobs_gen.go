// Code generated from knobs.yaml by go generate; DO NOT EDIT.

package knobs

import (
	"time"

	"github.com/htcondor/golang-htcondor/config"
)

// AnyUserReadGroups is HTTP_API_ANYUSER_READ_GROUPS.
//
// Groups whose members may request the anyuser:read entitlement, which
// reads every user's jobs on this access point rather than only the
// caller's own. Comma-separated, or a JSON array to spell a group name
// containing a comma. Unset means nobody may request it, so the
// entitlement is off until this or HTTP_API_ANYUSER_READ_USERS names
// somebody. Membership is permission to ask for the entitlement, never
// the entitlement itself: a credential holds it only by requesting it
// and being granted it.
var AnyUserReadGroups = config.DefineString("HTTP_API_ANYUSER_READ_GROUPS", "", config.Reconfig,
	"Groups whose members may request the anyuser:read entitlement, which reads every user's jobs on this access point rather than only the caller's own. Comma-separated, or a JSON array to spell a group name containing a comma. Unset means nobody may request it, so the entitlement is off until this or HTTP_API_ANYUSER_READ_USERS names somebody. Membership is permission to ask for the entitlement, never the entitlement itself: a credential holds it only by requesting it and being granted it.")

// AnyUserReadUsers is HTTP_API_ANYUSER_READ_USERS.
//
// Users who may request the anyuser:read entitlement, named
// individually rather than through a group -- which is the only half
// that works where HTTP_API_GROUP_SOURCE is token and the token
// carries no groups. Identities are compared the way HTCondor compares
// two users: the domain case-insensitively, the part before the @
// exactly, and a bare name matches only a bare name. Write
// alice@example.org, not alice, where the access point reports
// fully-qualified users.
var AnyUserReadUsers = config.DefineString("HTTP_API_ANYUSER_READ_USERS", "", config.Reconfig,
	"Users who may request the anyuser:read entitlement, named individually rather than through a group -- which is the only half that works where HTTP_API_GROUP_SOURCE is token and the token carries no groups. Identities are compared the way HTCondor compares two users: the domain case-insensitively, the part before the @ exactly, and a bare name matches only a bare name. Write alice@example.org, not alice, where the access point reports fully-qualified users.")

// AnyUserWriteGroups is HTTP_API_ANYUSER_WRITE_GROUPS.
//
// Groups whose members may request the anyuser:write entitlement,
// which removes, holds, releases and edits any user's jobs on this
// access point. Deliberately not implied by anyuser:read and not
// implying it: seeing every job and being able to remove every job are
// different privileges, and the group that should hold the second is
// usually much smaller. Unset means nobody may request it, here or in
// HTTP_API_ANYUSER_WRITE_USERS.
var AnyUserWriteGroups = config.DefineString("HTTP_API_ANYUSER_WRITE_GROUPS", "", config.Reconfig,
	"Groups whose members may request the anyuser:write entitlement, which removes, holds, releases and edits any user's jobs on this access point. Deliberately not implied by anyuser:read and not implying it: seeing every job and being able to remove every job are different privileges, and the group that should hold the second is usually much smaller. Unset means nobody may request it, here or in HTTP_API_ANYUSER_WRITE_USERS.")

// AnyUserWriteUsers is HTTP_API_ANYUSER_WRITE_USERS.
//
// Users who may request the anyuser:write entitlement, named
// individually rather than through a group. Identities are compared as
// for HTTP_API_ANYUSER_READ_USERS.
var AnyUserWriteUsers = config.DefineString("HTTP_API_ANYUSER_WRITE_USERS", "", config.Reconfig,
	"Users who may request the anyuser:write entitlement, named individually rather than through a group. Identities are compared as for HTTP_API_ANYUSER_READ_USERS.")

// BuildDefaultCPUs is HTTP_API_BUILD_DEFAULT_CPUS.
//
// CPUs a container build requests when the caller does not say. A
// value that is not a whole number, or is negative, is reported and
// ignored rather than refusing to start: the rest of the server has
// nothing to do with container builds, and a typo in a build setting
// should not take the API down.
var BuildDefaultCPUs = config.DefineInt("HTTP_API_BUILD_DEFAULT_CPUS", 0, config.Startup,
	"CPUs a container build requests when the caller does not say. A value that is not a whole number, or is negative, is reported and ignored rather than refusing to start: the rest of the server has nothing to do with container builds, and a typo in a build setting should not take the API down.")

// BuildDefaultDiskMB is HTTP_API_BUILD_DEFAULT_DISK_MB.
//
// Disk in MiB a container build requests when the caller does not say.
// Ignored with a report when it is not a non-negative whole number, as
// above.
var BuildDefaultDiskMB = config.DefineInt("HTTP_API_BUILD_DEFAULT_DISK_MB", 0, config.Startup,
	"Disk in MiB a container build requests when the caller does not say. Ignored with a report when it is not a non-negative whole number, as above.")

// BuildDefaultMemoryMB is HTTP_API_BUILD_DEFAULT_MEMORY_MB.
//
// Memory in MiB a container build requests when the caller does not
// say. Ignored with a report when it is not a non-negative whole
// number, as above.
var BuildDefaultMemoryMB = config.DefineInt("HTTP_API_BUILD_DEFAULT_MEMORY_MB", 0, config.Startup,
	"Memory in MiB a container build requests when the caller does not say. Ignored with a report when it is not a non-negative whole number, as above.")

// BuildExtraSubmit is HTTP_API_BUILD_EXTRA_SUBMIT.
//
// Extra submit-file lines added to every container build job, for
// whatever else a site's build slots need.
var BuildExtraSubmit = config.DefineString("HTTP_API_BUILD_EXTRA_SUBMIT", "", config.Startup,
	"Extra submit-file lines added to every container build job, for whatever else a site's build slots need.")

// BuildMaxCPUs is HTTP_API_BUILD_MAX_CPUS.
//
// Most CPUs one container build may request; a larger request is
// clamped to it rather than refused. Worth setting, because build
// slots are large -- CHTC's are 128-core, 256 GB machines -- and an
// agent asking for plenty should not be able to claim a whole one.
var BuildMaxCPUs = config.DefineInt("HTTP_API_BUILD_MAX_CPUS", 0, config.Startup,
	"Most CPUs one container build may request; a larger request is clamped to it rather than refused. Worth setting, because build slots are large -- CHTC's are 128-core, 256 GB machines -- and an agent asking for plenty should not be able to claim a whole one.")

// BuildMaxDiskMB is HTTP_API_BUILD_MAX_DISK_MB.
//
// Most disk in MiB one container build may request, clamped rather
// than refused, for the reason above.
var BuildMaxDiskMB = config.DefineInt("HTTP_API_BUILD_MAX_DISK_MB", 0, config.Startup,
	"Most disk in MiB one container build may request, clamped rather than refused, for the reason above.")

// BuildMaxMemoryMB is HTTP_API_BUILD_MAX_MEMORY_MB.
//
// Most memory in MiB one container build may request, clamped rather
// than refused, for the reason above.
var BuildMaxMemoryMB = config.DefineInt("HTTP_API_BUILD_MAX_MEMORY_MB", 0, config.Startup,
	"Most memory in MiB one container build may request, clamped rather than refused, for the reason above.")

// BuildRequirements is HTTP_API_BUILD_REQUIREMENTS.
//
// ClassAd expression selecting the slots a container build may run on,
// for a site where no schedd transform does it. A site picks its build
// machines by its own convention and the tool cannot guess it, so an
// unconfigured server submits an ordinary job and relies on the caller
// naming a destination.
var BuildRequirements = config.DefineString("HTTP_API_BUILD_REQUIREMENTS", "", config.Startup,
	"ClassAd expression selecting the slots a container build may run on, for a site where no schedd transform does it. A site picks its build machines by its own convention and the tool cannot guess it, so an unconfigured server submits an ordinary job and relies on the caller naming a destination.")

// BuildStagingBase is HTTP_API_BUILD_STAGING_BASE.
//
// Where built images are published when the caller gives a bare name.
// Unset means every call has to name its own destination. It may be a
// template: {user} expands to the authenticated caller and {initial}
// to the first letter of that name, so a per-user staging area can be
// written osdf:///chtc/staging/{initial}/{user}. A base with no
// placeholder is used verbatim.
var BuildStagingBase = config.DefineString("HTTP_API_BUILD_STAGING_BASE", "", config.Startup,
	"Where built images are published when the caller gives a bare name. Unset means every call has to name its own destination. It may be a template: {user} expands to the authenticated caller and {initial} to the first letter of that name, so a per-user staging area can be written osdf:///chtc/staging/{initial}/{user}. A base with no placeholder is used verbatim.")

// ConnectorOutputSweepInterval is HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL.
//
// How often finished jobs are swept for output to deliver to a
// connected account, as a duration with a unit. A bare number is
// refused rather than guessed at, and anything under a minute is
// raised to one with a message: sweeping faster asks the queue and the
// provider about the same finished jobs more often than they can
// change, and a provider's rate limit is a real consequence. Read at
// startup; the running ticker does not re-read it, so a change needs a
// restart even though the rest of the connector configuration reloads.
var ConnectorOutputSweepInterval = config.DefineDuration("HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL", time.Duration(0) /* 0s */, config.Startup,
	"How often finished jobs are swept for output to deliver to a connected account, as a duration with a unit. A bare number is refused rather than guessed at, and anything under a minute is raised to one with a message: sweeping faster asks the queue and the provider about the same finished jobs more often than they can change, and a provider's rate limit is a real consequence. Read at startup; the running ticker does not re-read it, so a change needs a restart even though the rest of the connector configuration reloads.")

// ConnectorPrefix is HTTP_API_CONNECTOR_PREFIX.
//
// The prefix of the credd service names this server stores
// connected-account credentials under. Change it only to keep two
// servers sharing one credd from seeing each other's credentials.
var ConnectorPrefix = config.DefineString("HTTP_API_CONNECTOR_PREFIX", "webapi", config.Startup,
	"The prefix of the credd service names this server stores connected-account credentials under. Change it only to keep two servers sharing one credd from seeing each other's credentials.")

// EnableIDP is HTTP_API_ENABLE_IDP.
//
// Run the built-in identity provider, so this server signs users in
// itself instead of sending them to an external one. Off by default.
// The provider shares the unified application database; the separate
// HTTP_API_IDP_DB_PATH it once had is no longer read.
var EnableIDP = config.DefineBool("HTTP_API_ENABLE_IDP", false, config.Startup,
	"Run the built-in identity provider, so this server signs users in itself instead of sending them to an external one. Off by default. The provider shares the unified application database; the separate HTTP_API_IDP_DB_PATH it once had is no longer read.")

// IdempotencyKeyCheck is HTTP_API_IDEMPOTENCY_KEY_CHECK.
//
// How much of the idempotency-key guarantee this access point
// enforces. NONE does not check at all; QUEUE checks the live job
// queue; FULL checks the queue and the mirrored history, which is what
// makes a key reused after its job finished still a duplicate; AUTO is
// FULL where an htcondordb mirror is configured and QUEUE where none
// is. OFF and FALSE read as NONE, TRUE as FULL. Anything else is
// reported and AUTO applies.
var IdempotencyKeyCheck = config.DefineString("HTTP_API_IDEMPOTENCY_KEY_CHECK", "", config.Startup,
	"How much of the idempotency-key guarantee this access point enforces. NONE does not check at all; QUEUE checks the live job queue; FULL checks the queue and the mirrored history, which is what makes a key reused after its job finished still a duplicate; AUTO is FULL where an htcondordb mirror is configured and QUEUE where none is. OFF and FALSE read as NONE, TRUE as FULL. Anything else is reported and AUTO applies.")

// IdempotencyKeyHistoryWindow is HTTP_API_IDEMPOTENCY_KEY_HISTORY_WINDOW.
//
// How far back, in whole seconds, the history lookup reads, as a floor
// on EnteredHistoryTime. It trades the guarantee for the cost of the
// read: a key reused after the window reads as unused. Unset consults
// the whole history, which is what the index is there for, so set this
// only where that read is measurably too expensive.
var IdempotencyKeyHistoryWindow = config.DefineInt("HTTP_API_IDEMPOTENCY_KEY_HISTORY_WINDOW", 0, config.Startup,
	"How far back, in whole seconds, the history lookup reads, as a floor on EnteredHistoryTime. It trades the guarantee for the cost of the read: a key reused after the window reads as unused. Unset consults the whole history, which is what the index is there for, so set this only where that read is measurably too expensive.")

// IdempotencyKeyRetention is HTTP_API_IDEMPOTENCY_KEY_RETENTION.
//
// How long, in whole seconds, a key this server submitted stays
// claimed in memory. It covers the window in which a finished job is
// in neither the live queue nor the mirrored history yet, so it wants
// to be longer than the gap between the schedd writing a job out and
// the mirror carrying it. A value that is not a non-negative whole
// number is reported and the default applies.
var IdempotencyKeyRetention = config.DefineInt("HTTP_API_IDEMPOTENCY_KEY_RETENTION", 0, config.Startup,
	"How long, in whole seconds, a key this server submitted stays claimed in memory. It covers the window in which a finished job is in neither the live queue nor the mirrored history yet, so it wants to be longer than the gap between the schedd writing a job out and the mirror carrying it. A value that is not a non-negative whole number is reported and the default applies.")

// IdempotencyKeyTimeout is HTTP_API_IDEMPOTENCY_KEY_TIMEOUT.
//
// How long, in whole seconds, the queue and history lookups get
// between them. A keyed submission waits on them, so this is also the
// longest the check can delay one. Reported and defaulted the same way
// as above when it is not a non-negative whole number.
var IdempotencyKeyTimeout = config.DefineInt("HTTP_API_IDEMPOTENCY_KEY_TIMEOUT", 0, config.Startup,
	"How long, in whole seconds, the queue and history lookups get between them. A keyed submission waits on them, so this is also the longest the check can delay one. Reported and defaulted the same way as above when it is not a non-negative whole number.")

// IdleTimeout is HTTP_API_IDLE_TIMEOUT.
//
// How long a keep-alive connection may sit idle before the server
// closes it, as a Go duration.
var IdleTimeout = config.DefineDuration("HTTP_API_IDLE_TIMEOUT", time.Duration(120000000000) /* 120s */, config.Startup,
	"How long a keep-alive connection may sit idle before the server closes it, as a Go duration.")

// IDPAccessTokenLifespan is HTTP_API_IDP_ACCESS_TOKEN_LIFESPAN.
//
// How long an access token the built-in identity provider issues stays
// valid, as a Go duration. A value that is not a duration, or is not
// positive, refuses to start rather than being quietly ignored -- a
// token lifetime is not something to get wrong silently. Read only
// when HTTP_API_ENABLE_IDP is on.
var IDPAccessTokenLifespan = config.DefineDuration("HTTP_API_IDP_ACCESS_TOKEN_LIFESPAN", time.Duration(0) /* 0s */, config.Startup,
	"How long an access token the built-in identity provider issues stays valid, as a Go duration. A value that is not a duration, or is not positive, refuses to start rather than being quietly ignored -- a token lifetime is not something to get wrong silently. Read only when HTTP_API_ENABLE_IDP is on.")

// IDPIssuer is HTTP_API_IDP_ISSUER.
//
// The issuer the built-in identity provider names in its own
// documents. Unset derives it the same way HTTP_API_OAUTH2_ISSUER is
// derived, from the externally-visible URL. Read only when
// HTTP_API_ENABLE_IDP is on.
var IDPIssuer = config.DefineString("HTTP_API_IDP_ISSUER", "", config.Startup,
	"The issuer the built-in identity provider names in its own documents. Unset derives it the same way HTTP_API_OAUTH2_ISSUER is derived, from the externally-visible URL. Read only when HTTP_API_ENABLE_IDP is on.")

// IDPRefreshTokenLifespan is HTTP_API_IDP_REFRESH_TOKEN_LIFESPAN.
//
// How long a refresh token the built-in identity provider issues stays
// valid. It has to be at least the access token lifespan, or the
// provider refuses to start: a refresh token that dies first makes
// every refresh fail after the access token expires. Read only when
// HTTP_API_ENABLE_IDP is on, and refused the same way as above when it
// is not a positive duration.
var IDPRefreshTokenLifespan = config.DefineDuration("HTTP_API_IDP_REFRESH_TOKEN_LIFESPAN", time.Duration(0) /* 0s */, config.Startup,
	"How long a refresh token the built-in identity provider issues stays valid. It has to be at least the access token lifespan, or the provider refuses to start: a refresh token that dies first makes every refresh fail after the access token expires. Read only when HTTP_API_ENABLE_IDP is on, and refused the same way as above when it is not a positive duration.")

// InteractiveExtraSubmit is HTTP_API_INTERACTIVE_EXTRA_SUBMIT.
//
// Extra submit-file lines added to every interactive session job --
// terminal, VS Code and JupyterLab. Operator-only configuration,
// spliced in as written, as with the submit-file settings above.
var InteractiveExtraSubmit = config.DefineString("HTTP_API_INTERACTIVE_EXTRA_SUBMIT", "", config.Startup,
	"Extra submit-file lines added to every interactive session job -- terminal, VS Code and JupyterLab. Operator-only configuration, spliced in as written, as with the submit-file settings above.")

// InteractiveJupyter is HTTP_API_INTERACTIVE_JUPYTER.
//
// Offer JupyterLab sessions. On by default. Turning it off refuses new
// ones while sessions already running keep working, so they drain
// rather than being dropped -- which is why it is applied by a
// reconfigure and not by a restart. A value that is not a boolean
// refuses to start. See "Which applications this access point offers"
// in the operator guide.
var InteractiveJupyter = config.DefineBool("HTTP_API_INTERACTIVE_JUPYTER", true, config.Reconfig,
	"Offer JupyterLab sessions. On by default. Turning it off refuses new ones while sessions already running keep working, so they drain rather than being dropped -- which is why it is applied by a reconfigure and not by a restart. A value that is not a boolean refuses to start. See \"Which applications this access point offers\" in the operator guide.")

// InteractiveRequirements is HTTP_API_INTERACTIVE_REQUIREMENTS.
//
// ClassAd expression ANDed into the Requirements of every interactive
// session job. A terminal is only worth starting on a machine it can
// be attached to, and whether that works is a property of the machine.
// An expression that does not parse refuses to start rather than being
// dropped, since a dropped requirement sends sessions to machines they
// cannot be used on.
var InteractiveRequirements = config.DefineString("HTTP_API_INTERACTIVE_REQUIREMENTS", "", config.Startup,
	"ClassAd expression ANDed into the Requirements of every interactive session job. A terminal is only worth starting on a machine it can be attached to, and whether that works is a property of the machine. An expression that does not parse refuses to start rather than being dropped, since a dropped requirement sends sessions to machines they cannot be used on.")

// InteractiveTerminal is HTTP_API_INTERACTIVE_TERMINAL.
//
// Offer terminal sessions. On by default, and otherwise as above.
// Turning it off also withdraws the MCP interactive_session_start
// tool, which starts the same thing.
var InteractiveTerminal = config.DefineBool("HTTP_API_INTERACTIVE_TERMINAL", true, config.Reconfig,
	"Offer terminal sessions. On by default, and otherwise as above. Turning it off also withdraws the MCP interactive_session_start tool, which starts the same thing.")

// InteractiveVSCode is HTTP_API_INTERACTIVE_VSCODE.
//
// Offer VS Code sessions. On by default, and otherwise exactly as
// above.
var InteractiveVSCode = config.DefineBool("HTTP_API_INTERACTIVE_VSCODE", true, config.Reconfig,
	"Offer VS Code sessions. On by default, and otherwise exactly as above.")

// JobQueueLog is HTTP_API_JOB_QUEUE_LOG.
//
// The schedd's job queue log, tailed to serve the job change stream,
// or the word none to switch the stream off. Unset falls back to
// JOB_QUEUE_LOG where the file is there, which is the case when this
// server shares a host with its schedd; where it does not -- an API
// server in its own container, the usual deployment -- there is
// nothing to tail and that is not a misconfiguration. Named explicitly
// and absent is an operator error, and is reported as one.
var JobQueueLog = config.DefineString("HTTP_API_JOB_QUEUE_LOG", "", config.Startup,
	"The schedd's job queue log, tailed to serve the job change stream, or the word none to switch the stream off. Unset falls back to JOB_QUEUE_LOG where the file is there, which is the case when this server shares a host with its schedd; where it does not -- an API server in its own container, the usual deployment -- there is nothing to tail and that is not a misconfiguration. Named explicitly and absent is an operator error, and is reported as one.")

// JupyterKernelIdleSec is HTTP_API_JUPYTER_KERNEL_IDLE_SEC.
//
// How long in whole seconds a JupyterLab kernel may sit without
// executing before it is culled and the server shuts down. Measured
// from kernel execution rather than HTTP traffic, so a tab polling in
// the background does not look busy. Zero disables it, and an
// unreadable value is reported as above.
var JupyterKernelIdleSec = config.DefineInt("HTTP_API_JUPYTER_KERNEL_IDLE_SEC", 3600, config.Startup,
	"How long in whole seconds a JupyterLab kernel may sit without executing before it is culled and the server shuts down. Measured from kernel execution rather than HTTP traffic, so a tab polling in the background does not look busy. Zero disables it, and an unreadable value is reported as above.")

// JupyterMaxLifetimeSec is HTTP_API_JUPYTER_MAX_LIFETIME_SEC.
//
// Wall-clock ceiling on a JupyterLab session in whole seconds, counted
// from when it starts running. Enforced by the schedd through
// periodic_remove, so it holds whatever the sandbox or the browser are
// doing. Zero disables it. A value that is not a non-negative whole
// number is reported and the default applies.
var JupyterMaxLifetimeSec = config.DefineInt("HTTP_API_JUPYTER_MAX_LIFETIME_SEC", 28800, config.Startup,
	"Wall-clock ceiling on a JupyterLab session in whole seconds, counted from when it starts running. Enforced by the schedd through periodic_remove, so it holds whatever the sandbox or the browser are doing. Zero disables it. A value that is not a non-negative whole number is reported and the default applies.")

// JupyterReconnectGraceSec is HTTP_API_JUPYTER_RECONNECT_GRACE_SEC.
//
// How long in whole seconds a session whose tunnel dropped is kept for
// its helper to dial back. Meanwhile the session stays listed and its
// proxy answers 503 with Retry-After; past it the session is closed
// and a late redial is refused, which ends the job. Not turnable off
// either: zero, negative and unreadable values fall back to the
// default and anything above 3600 is clamped, each with a report.
var JupyterReconnectGraceSec = config.DefineInt("HTTP_API_JUPYTER_RECONNECT_GRACE_SEC", 300, config.Startup,
	"How long in whole seconds a session whose tunnel dropped is kept for its helper to dial back. Meanwhile the session stays listed and its proxy answers 503 with Retry-After; past it the session is closed and a late redial is refused, which ends the job. Not turnable off either: zero, negative and unreadable values fall back to the default and anything above 3600 is clamped, each with a report.")

// JupyterStartGraceSec is HTTP_API_JUPYTER_START_GRACE_SEC.
//
// How long in whole seconds a JupyterLab job may wait to start
// executing, counted from submission. Past it the schedd removes the
// job and the token it would have connected with expires ten minutes
// later, so a job never starts holding a token that is already dead. A
// job that ran and went back to the queue -- evicted, or held and
// released -- is removed too, because it would rerun with a token its
// first run spent. This cannot be turned off: zero, negative and
// unreadable values fall back to the default and anything above 604800
// (seven days) is clamped, each with a report.
var JupyterStartGraceSec = config.DefineInt("HTTP_API_JUPYTER_START_GRACE_SEC", 14400, config.Startup,
	"How long in whole seconds a JupyterLab job may wait to start executing, counted from submission. Past it the schedd removes the job and the token it would have connected with expires ten minutes later, so a job never starts holding a token that is already dead. A job that ran and went back to the queue -- evicted, or held and released -- is removed too, because it would rerun with a token its first run spent. This cannot be turned off: zero, negative and unreadable values fall back to the default and anything above 604800 (seven days) is clamped, each with a report.")

// JupyterWorkDir is HTTP_API_JUPYTER_WORK_DIR.
//
// Where the helper binary and token files for JupyterLab submissions
// are staged. The helper itself is built into this binary, so nothing
// has to be found on disk; this is only where it is written out to be
// transferred.
var JupyterWorkDir = config.DefineString("HTTP_API_JUPYTER_WORK_DIR", "", config.Startup,
	"Where the helper binary and token files for JupyterLab submissions are staged. The helper itself is built into this binary, so nothing has to be found on disk; this is only where it is written out to be transferred.")

// Listen is HTTP_API_LISTEN.
//
// Obsolete: superseded by HTTP_API_LISTEN_ADDR. An older spelling of
// the listen address, honored when HTTP_API_LISTEN_ADDR is unset and
// reported as an alias when it is used. HTTP_API_LISTEN_ADDR wins
// where both are set.
//
// Deprecated: superseded by HTTP_API_LISTEN_ADDR.
var Listen = config.DefineString("HTTP_API_LISTEN", "", config.Startup,
	"Obsolete: superseded by HTTP_API_LISTEN_ADDR. An older spelling of the listen address, honored when HTTP_API_LISTEN_ADDR is unset and reported as an alias when it is used. HTTP_API_LISTEN_ADDR wins where both are set.")

// LLMAPIKeyFile is HTTP_API_LLM_API_KEY_FILE.
//
// File holding the API key for the model this server talks to. Setting
// it is what enables the chat assistant. The file must not be
// world-accessible: 0600, 0400 or 0440, the last being what a
// Kubernetes secret volume is under fsGroup. Holds or names a
// credential: keep it readable only by the account the server runs as.
var LLMAPIKeyFile = config.DefineString("HTTP_API_LLM_API_KEY_FILE", "", config.Startup,
	"File holding the API key for the model this server talks to. Setting it is what enables the chat assistant. The file must not be world-accessible: 0600, 0400 or 0440, the last being what a Kubernetes secret volume is under fsGroup. Holds or names a credential: keep it readable only by the account the server runs as.")

// LLMAPIURL is HTTP_API_LLM_API_URL.
//
// The full URL of the upstream messages endpoint, for a proxy or
// gateway in place of the vendor's own. Unset talks to the vendor
// directly.
var LLMAPIURL = config.DefineString("HTTP_API_LLM_API_URL", "", config.Startup,
	"The full URL of the upstream messages endpoint, for a proxy or gateway in place of the vendor's own. Unset talks to the vendor directly.")

// LLMAuthScheme is HTTP_API_LLM_AUTH_SCHEME.
//
// How the key is presented upstream: x-api-key, which is what the
// vendor's own API reads, or bearer, which is what an OpenAI-style
// gateway fronting it authenticates.
var LLMAuthScheme = config.DefineString("HTTP_API_LLM_AUTH_SCHEME", "", config.Startup,
	"How the key is presented upstream: x-api-key, which is what the vendor's own API reads, or bearer, which is what an OpenAI-style gateway fronting it authenticates.")

// LLMModel is HTTP_API_LLM_MODEL.
//
// The model to use, as the upstream knows it -- which behind a gateway
// is the gateway's name for it rather than the vendor's. Unset uses
// the built-in default.
var LLMModel = config.DefineString("HTTP_API_LLM_MODEL", "", config.Startup,
	"The model to use, as the upstream knows it -- which behind a gateway is the gateway's name for it rather than the vendor's. Unset uses the built-in default.")

// LLMOperatorInstructionsFile is HTTP_API_LLM_OPERATOR_INSTRUCTIONS_FILE.
//
// File of site-policy text appended to every chat system prompt, last,
// so that it outranks the standard prompts. Re-read on reconfigure
// whether or not the path changed, and a read that fails keeps the
// previous text rather than dropping the policy. Like the key file it
// must not be world-accessible. Holds or names a credential: keep it
// readable only by the account the server runs as.
var LLMOperatorInstructionsFile = config.DefineString("HTTP_API_LLM_OPERATOR_INSTRUCTIONS_FILE", "", config.Reconfig,
	"File of site-policy text appended to every chat system prompt, last, so that it outranks the standard prompts. Re-read on reconfigure whether or not the path changed, and a read that fails keeps the previous text rather than dropping the policy. Like the key file it must not be world-accessible. Holds or names a credential: keep it readable only by the account the server runs as.")

// Log is HTTP_API_LOG.
//
// Where this daemon writes its log: a file, or the words stdout or
// stderr. A named file that cannot be written falls back to stdout
// with a message -- except under condor_master, where stdout is
// discarded and there would be nowhere to report anything, so the
// daemon refuses to start and the reason goes to the master's log
// instead.
var Log = config.DefineString("HTTP_API_LOG", "", config.Startup,
	"Where this daemon writes its log: a file, or the words stdout or stderr. A named file that cannot be written falls back to stdout with a message -- except under condor_master, where stdout is discarded and there would be nowhere to report anything, so the daemon refuses to start and the reason goes to the master's log instead.")

// MCPAccessGroup is HTTP_API_MCP_ACCESS_GROUP.
//
// Obsolete: superseded by HTTP_API_MCP_ACCESS_GROUPS. An older
// spelling of HTTP_API_MCP_ACCESS_GROUPS, honored where that is unset
// and named as superseded in the startup log when it is read. It
// already accepted a list, which is why the replacement is plural.
//
// Deprecated: superseded by HTTP_API_MCP_ACCESS_GROUPS.
var MCPAccessGroup = config.DefineString("HTTP_API_MCP_ACCESS_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_MCP_ACCESS_GROUPS. An older spelling of HTTP_API_MCP_ACCESS_GROUPS, honored where that is unset and named as superseded in the startup log when it is read. It already accepted a list, which is why the replacement is plural.")

// MCPAccessGroups is HTTP_API_MCP_ACCESS_GROUPS.
//
// Groups whose members may reach /mcp at all. Comma-separated, or a
// JSON array to spell a group name containing a comma. Unset, with
// HTTP_API_MCP_ACCESS_USERS unset too, places no requirement on the
// surface and every authenticated caller reaches it; naming anybody in
// either half restricts it to the people named. See "Authorization
// groups" in the operator guide.
var MCPAccessGroups = config.DefineString("HTTP_API_MCP_ACCESS_GROUPS", "", config.Reconfig,
	"Groups whose members may reach /mcp at all. Comma-separated, or a JSON array to spell a group name containing a comma. Unset, with HTTP_API_MCP_ACCESS_USERS unset too, places no requirement on the surface and every authenticated caller reaches it; naming anybody in either half restricts it to the people named. See \"Authorization groups\" in the operator guide.")

// MCPAccessUsers is HTTP_API_MCP_ACCESS_USERS.
//
// Users who may reach /mcp, named individually rather than through a
// group -- which is the only half that decides anything where
// HTTP_API_GROUP_SOURCE is token and the token carries no groups.
// Identities are compared the way HTCondor compares two users: the
// domain case-insensitively, the part before the @ exactly, and a bare
// name matches only a bare name. Write alice@example.org, not alice,
// where the access point reports fully-qualified users. Naming
// somebody here restricts the surface to the people named, here or in
// HTTP_API_MCP_ACCESS_GROUPS. See "Authorization groups" in the
// operator guide.
var MCPAccessUsers = config.DefineString("HTTP_API_MCP_ACCESS_USERS", "", config.Reconfig,
	"Users who may reach /mcp, named individually rather than through a group -- which is the only half that decides anything where HTTP_API_GROUP_SOURCE is token and the token carries no groups. Identities are compared the way HTCondor compares two users: the domain case-insensitively, the part before the @ exactly, and a bare name matches only a bare name. Write alice@example.org, not alice, where the access point reports fully-qualified users. Naming somebody here restricts the surface to the people named, here or in HTTP_API_MCP_ACCESS_GROUPS. See \"Authorization groups\" in the operator guide.")

// MCPAdminGroup is HTTP_API_MCP_ADMIN_GROUP.
//
// Obsolete: superseded by HTTP_API_ANYUSER_READ_GROUPS. Group(s) whose
// members may read every user's jobs. Honored where
// HTTP_API_ANYUSER_READ_GROUPS is unset, and named as superseded in
// the startup log when it is read -- which is the only place an
// operator still running it meets the replacement, since this setting
// appears in no page. Read as a raw string and split downstream, so a
// change to how lists are parsed does not change who is privileged.
//
// Deprecated: superseded by HTTP_API_ANYUSER_READ_GROUPS.
var MCPAdminGroup = config.DefineString("HTTP_API_MCP_ADMIN_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_ANYUSER_READ_GROUPS. Group(s) whose members may read every user's jobs. Honored where HTTP_API_ANYUSER_READ_GROUPS is unset, and named as superseded in the startup log when it is read -- which is the only place an operator still running it meets the replacement, since this setting appears in no page. Read as a raw string and split downstream, so a change to how lists are parsed does not change who is privileged.")

// MCPBaseURL is HTTP_API_MCP_BASE_URL.
//
// The externally-visible base URL of the MCP listener, for a
// deployment that serves MCP on an address of its own. Unset, MCP is
// reached at HTTP_API_BASE_URL like everything else.
var MCPBaseURL = config.DefineString("HTTP_API_MCP_BASE_URL", "", config.Startup,
	"The externally-visible base URL of the MCP listener, for a deployment that serves MCP on an address of its own. Unset, MCP is reached at HTTP_API_BASE_URL like everything else.")

// MCPConfirmRemoveOver is HTTP_API_MCP_CONFIRM_REMOVE_OVER.
//
// The same, for the number of jobs remove_jobs would remove. Zero is
// off. Counting the matches costs one extra queue query per gated
// call, which is why it is not on by default.
var MCPConfirmRemoveOver = config.DefineInt("HTTP_API_MCP_CONFIRM_REMOVE_OVER", 0, config.Reconfig,
	"The same, for the number of jobs remove_jobs would remove. Zero is off. Counting the matches costs one extra queue query per gated call, which is why it is not on by default.")

// MCPConfirmSubmitOver is HTTP_API_MCP_CONFIRM_SUBMIT_OVER.
//
// Ask the person running the client to confirm before submit_job
// submits more than this many jobs. Zero, the default, is off. This is
// a prompt and not a limit: a client that cannot present the question
// -- including any client older than MCP protocol 2026-07-28 --
// proceeds unasked, logged at WARN. See "Asking a person before a bulk
// operation" in the operator guide.
var MCPConfirmSubmitOver = config.DefineInt("HTTP_API_MCP_CONFIRM_SUBMIT_OVER", 0, config.Reconfig,
	"Ask the person running the client to confirm before submit_job submits more than this many jobs. Zero, the default, is off. This is a prompt and not a limit: a client that cannot present the question -- including any client older than MCP protocol 2026-07-28 -- proceeds unasked, logged at WARN. See \"Asking a person before a bulk operation\" in the operator guide.")

// MCPDisabledTools is HTTP_API_MCP_DISABLED_TOOLS.
//
// MCP tools this access point does not offer, as glob patterns
// separated by commas or whitespace. See "Disabling tools" in the
// operator guide.
var MCPDisabledTools = config.DefineString("HTTP_API_MCP_DISABLED_TOOLS", "", config.Reconfig,
	"MCP tools this access point does not offer, as glob patterns separated by commas or whitespace. See \"Disabling tools\" in the operator guide.")

// MCPListenAddr is HTTP_API_MCP_LISTEN_ADDR.
//
// A second address to serve MCP on, separate from
// HTTP_API_LISTEN_ADDR. Unset serves MCP on the one listener, which is
// what almost every deployment wants; this is for separating the agent
// surface onto its own port or interface.
var MCPListenAddr = config.DefineString("HTTP_API_MCP_LISTEN_ADDR", "", config.Startup,
	"A second address to serve MCP on, separate from HTTP_API_LISTEN_ADDR. Unset serves MCP on the one listener, which is what almost every deployment wants; this is for separating the agent surface onto its own port or interface.")

// MCPMaxRequestDuration is HTTP_API_MCP_MAX_REQUEST_DURATION.
//
// Hard stop on an MCP request that is still making progress. While one
// runs its write deadline is moved forward, so HTTP_API_WRITE_TIMEOUT
// -- an absolute deadline meant for ordinary replies -- does not
// decide how long a deliberately waiting tool may wait. A request that
// stops making progress is still cut off on its last window; this is
// the backstop for one that never finishes at all. It needs a unit,
// and a value without one refuses to start.
var MCPMaxRequestDuration = config.DefineDuration("HTTP_API_MCP_MAX_REQUEST_DURATION", time.Duration(0) /* 0s */, config.Startup,
	"Hard stop on an MCP request that is still making progress. While one runs its write deadline is moved forward, so HTTP_API_WRITE_TIMEOUT -- an absolute deadline meant for ordinary replies -- does not decide how long a deliberately waiting tool may wait. A request that stops making progress is still cut off on its last window; this is the backstop for one that never finishes at all. It needs a unit, and a value without one refuses to start.")

// MCPReadGroup is HTTP_API_MCP_READ_GROUP.
//
// Obsolete: superseded by HTTP_API_MCP_READ_GROUPS. An older spelling
// of HTTP_API_MCP_READ_GROUPS, honored where that is unset and named
// as superseded in the startup log.
//
// Deprecated: superseded by HTTP_API_MCP_READ_GROUPS.
var MCPReadGroup = config.DefineString("HTTP_API_MCP_READ_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_MCP_READ_GROUPS. An older spelling of HTTP_API_MCP_READ_GROUPS, honored where that is unset and named as superseded in the startup log.")

// MCPReadGroups is HTTP_API_MCP_READ_GROUPS.
//
// Groups whose members are granted the mcp:read scope, which reads the
// caller's own jobs through /mcp. Unset, with HTTP_API_MCP_READ_USERS
// unset too, the scope follows the access gate instead: whoever may
// reach /mcp may read through it. See "Authorization groups" in the
// operator guide.
var MCPReadGroups = config.DefineString("HTTP_API_MCP_READ_GROUPS", "", config.Reconfig,
	"Groups whose members are granted the mcp:read scope, which reads the caller's own jobs through /mcp. Unset, with HTTP_API_MCP_READ_USERS unset too, the scope follows the access gate instead: whoever may reach /mcp may read through it. See \"Authorization groups\" in the operator guide.")

// MCPReadOnly is HTTP_API_MCP_READ_ONLY.
//
// Serve MCP with no tool that changes anything, for every caller
// whatever their scopes. Off by default. While it is on, mcp:write is
// neither advertised nor granted. See "A read-only access point" in
// the operator guide.
var MCPReadOnly = config.DefineBool("HTTP_API_MCP_READ_ONLY", false, config.Reconfig,
	"Serve MCP with no tool that changes anything, for every caller whatever their scopes. Off by default. While it is on, mcp:write is neither advertised nor granted. See \"A read-only access point\" in the operator guide.")

// MCPReadUsers is HTTP_API_MCP_READ_USERS.
//
// Users granted the mcp:read scope, named individually rather than
// through a group. Identities are compared as for
// HTTP_API_MCP_ACCESS_USERS. See "Authorization groups" in the
// operator guide.
var MCPReadUsers = config.DefineString("HTTP_API_MCP_READ_USERS", "", config.Reconfig,
	"Users granted the mcp:read scope, named individually rather than through a group. Identities are compared as for HTTP_API_MCP_ACCESS_USERS. See \"Authorization groups\" in the operator guide.")

// MCPRedactDisabledRules is HTTP_API_MCP_REDACT_DISABLED_RULES.
//
// Masking rules to turn off, as glob patterns over rule ids separated
// by commas or whitespace.
var MCPRedactDisabledRules = config.DefineString("HTTP_API_MCP_REDACT_DISABLED_RULES", "", config.Reconfig,
	"Masking rules to turn off, as glob patterns over rule ids separated by commas or whitespace.")

// MCPRedactSecrets is HTTP_API_MCP_REDACT_SECRETS.
//
// Mask credential-shaped text in MCP tool results. On by default, and
// worth leaving on: a tool result is read by a model and then often by
// a log. See "Masking credentials in tool results" in the operator
// guide.
var MCPRedactSecrets = config.DefineBool("HTTP_API_MCP_REDACT_SECRETS", true, config.Reconfig,
	"Mask credential-shaped text in MCP tool results. On by default, and worth leaving on: a tool result is read by a model and then often by a log. See \"Masking credentials in tool results\" in the operator guide.")

// MCPSkillsDir is HTTP_API_MCP_SKILLS_DIR.
//
// Directory of site-authored Markdown skills published to agents.
// Re-read periodically and on reconfigure, so a checkout updated in
// place is noticed without either. See "Site skills" in the operator
// guide.
var MCPSkillsDir = config.DefineString("HTTP_API_MCP_SKILLS_DIR", "", config.Reconfig,
	"Directory of site-authored Markdown skills published to agents. Re-read periodically and on reconfigure, so a checkout updated in place is noticed without either. See \"Site skills\" in the operator guide.")

// MCPSkillsReloadInterval is HTTP_API_MCP_SKILLS_RELOAD_INTERVAL.
//
// How often that directory is re-read, so a checkout updated
// underneath the daemon is noticed without a reconfigure. Zero
// disables the poll and leaves reloads to condor_reconfig. The check
// is stat-only until something actually changes. A value under one
// second is raised to one second rather than honoured: it would stat
// the tree hundreds of times a minute to learn nothing.
var MCPSkillsReloadInterval = config.DefineDuration("HTTP_API_MCP_SKILLS_RELOAD_INTERVAL", time.Duration(300000000000) /* 5m */, config.Startup,
	"How often that directory is re-read, so a checkout updated underneath the daemon is noticed without a reconfigure. Zero disables the poll and leaves reloads to condor_reconfig. The check is stat-only until something actually changes. A value under one second is raised to one second rather than honoured: it would stat the tree hundreds of times a minute to learn nothing.")

// MCPSuperuserGroup is HTTP_API_MCP_SUPERUSER_GROUP.
//
// Obsolete: superseded by HTTP_API_ANYUSER_WRITE_GROUPS. Group(s)
// whose members may change any user's jobs. Honored where
// HTTP_API_ANYUSER_WRITE_GROUPS is unset, and named as superseded in
// the startup log when it is read. Read as a raw string and split
// downstream, as above.
//
// Deprecated: superseded by HTTP_API_ANYUSER_WRITE_GROUPS.
var MCPSuperuserGroup = config.DefineString("HTTP_API_MCP_SUPERUSER_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_ANYUSER_WRITE_GROUPS. Group(s) whose members may change any user's jobs. Honored where HTTP_API_ANYUSER_WRITE_GROUPS is unset, and named as superseded in the startup log when it is read. Read as a raw string and split downstream, as above.")

// MCPWatchMaxWait is HTTP_API_MCP_WATCH_MAX_WAIT.
//
// Cap on how long the MCP check_watches and watch_jobs tools may block
// in-call before returning. This number is advertised to the agent in
// the tool schema, so it has to be one the whole path can deliver and
// not just this server. 45 seconds is not a server limit -- the server
// will hold a request far longer -- it is what an MCP client is
// observed to wait for before abandoning the call, which returns
// nothing at all to the agent rather than a timeout it could act on.
// Set it lower where a gateway or proxy with a tighter timeout sits in
// front of this server, and higher only where the clients reaching
// this deployment are known to be configured for a longer tool
// timeout. It needs a unit, as above.
var MCPWatchMaxWait = config.DefineDuration("HTTP_API_MCP_WATCH_MAX_WAIT", time.Duration(0) /* 0s */, config.Startup,
	"Cap on how long the MCP check_watches and watch_jobs tools may block in-call before returning. This number is advertised to the agent in the tool schema, so it has to be one the whole path can deliver and not just this server. 45 seconds is not a server limit -- the server will hold a request far longer -- it is what an MCP client is observed to wait for before abandoning the call, which returns nothing at all to the agent rather than a timeout it could act on. Set it lower where a gateway or proxy with a tighter timeout sits in front of this server, and higher only where the clients reaching this deployment are known to be configured for a longer tool timeout. It needs a unit, as above.")

// MCPWriteGroup is HTTP_API_MCP_WRITE_GROUP.
//
// Obsolete: superseded by HTTP_API_MCP_WRITE_GROUPS. An older spelling
// of HTTP_API_MCP_WRITE_GROUPS, honored where that is unset and named
// as superseded in the startup log.
//
// Deprecated: superseded by HTTP_API_MCP_WRITE_GROUPS.
var MCPWriteGroup = config.DefineString("HTTP_API_MCP_WRITE_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_MCP_WRITE_GROUPS. An older spelling of HTTP_API_MCP_WRITE_GROUPS, honored where that is unset and named as superseded in the startup log.")

// MCPWriteGroups is HTTP_API_MCP_WRITE_GROUPS.
//
// Groups whose members are granted the mcp:write scope -- and
// condor:/WRITE, which mints an IDTOKEN carrying the same
// authorization, so the two follow one policy rather than offering the
// same power under two names. Unset, with HTTP_API_MCP_WRITE_USERS
// unset too, the scope follows the access gate. See "Authorization
// groups" in the operator guide.
var MCPWriteGroups = config.DefineString("HTTP_API_MCP_WRITE_GROUPS", "", config.Reconfig,
	"Groups whose members are granted the mcp:write scope -- and condor:/WRITE, which mints an IDTOKEN carrying the same authorization, so the two follow one policy rather than offering the same power under two names. Unset, with HTTP_API_MCP_WRITE_USERS unset too, the scope follows the access gate. See \"Authorization groups\" in the operator guide.")

// MCPWriteUsers is HTTP_API_MCP_WRITE_USERS.
//
// Users granted the mcp:write scope, named individually rather than
// through a group. Identities are compared as for
// HTTP_API_MCP_ACCESS_USERS. See "Authorization groups" in the
// operator guide.
var MCPWriteUsers = config.DefineString("HTTP_API_MCP_WRITE_USERS", "", config.Reconfig,
	"Users granted the mcp:write scope, named individually rather than through a group. Identities are compared as for HTTP_API_MCP_ACCESS_USERS. See \"Authorization groups\" in the operator guide.")

// OAuth2AccessTokenLifespan is HTTP_API_OAUTH2_ACCESS_TOKEN_LIFESPAN.
//
// How long an access token this server issues as an OAuth2
// authorization server stays valid, as a Go duration. Refused the same
// way as the identity provider's when it is not a positive duration.
var OAuth2AccessTokenLifespan = config.DefineDuration("HTTP_API_OAUTH2_ACCESS_TOKEN_LIFESPAN", time.Duration(0) /* 0s */, config.Startup,
	"How long an access token this server issues as an OAuth2 authorization server stays valid, as a Go duration. Refused the same way as the identity provider's when it is not a positive duration.")

// OAuth2AuthURL is HTTP_API_OAUTH2_AUTH_URL.
//
// The provider's authorization endpoint, used when discovery is not
// configured or did not answer.
var OAuth2AuthURL = config.DefineString("HTTP_API_OAUTH2_AUTH_URL", "", config.Startup,
	"The provider's authorization endpoint, used when discovery is not configured or did not answer.")

// OAuth2ClientID is HTTP_API_OAUTH2_CLIENT_ID.
//
// The client id this server registered with that identity provider.
var OAuth2ClientID = config.DefineString("HTTP_API_OAUTH2_CLIENT_ID", "", config.Startup,
	"The client id this server registered with that identity provider.")

// OAuth2ClientSecretFile is HTTP_API_OAUTH2_CLIENT_SECRET_FILE.
//
// File holding the client secret for that registration. Read as root
// where the ordinary read is denied, since a secret staged on an
// access point is usually root-owned and this daemon has dropped
// privilege by then. An unreadable file is a warning and no secret,
// not a refusal to start. Holds or names a credential: keep it
// readable only by the account the server runs as.
var OAuth2ClientSecretFile = config.DefineString("HTTP_API_OAUTH2_CLIENT_SECRET_FILE", "", config.Startup,
	"File holding the client secret for that registration. Read as root where the ordinary read is denied, since a secret staged on an access point is usually root-owned and this daemon has dropped privilege by then. An unreadable file is a warning and no secret, not a refusal to start. Holds or names a credential: keep it readable only by the account the server runs as.")

// OAuth2DBPath is HTTP_API_OAUTH2_DB_PATH.
//
// Deprecated and ignored. The application database is
// HTTP_API_DB_PATH; the legacy oauth.db schema conflicts with the
// unified migration, so setting this is reported and has no effect.
var OAuth2DBPath = config.DefineString("HTTP_API_OAUTH2_DB_PATH", "", config.Startup,
	"Deprecated and ignored. The application database is HTTP_API_DB_PATH; the legacy oauth.db schema conflicts with the unified migration, so setting this is reported and has no effect.")

// OAuth2GroupsClaim is HTTP_API_OAUTH2_GROUPS_CLAIM.
//
// Which claim in the token carries the user's groups, for the settings
// that gate access on group membership.
var OAuth2GroupsClaim = config.DefineString("HTTP_API_OAUTH2_GROUPS_CLAIM", "groups", config.Startup,
	"Which claim in the token carries the user's groups, for the settings that gate access on group membership.")

// OAuth2IDP is HTTP_API_OAUTH2_IDP.
//
// An external identity provider to sign users in through, as the base
// URL of its OIDC discovery document. Discovery supplies the
// authorization, token and userinfo endpoints; the explicit
// HTTP_API_OAUTH2_*_URL settings are the fallback when it fails, which
// is logged as a warning rather than refused.
var OAuth2IDP = config.DefineString("HTTP_API_OAUTH2_IDP", "", config.Startup,
	"An external identity provider to sign users in through, as the base URL of its OIDC discovery document. Discovery supplies the authorization, token and userinfo endpoints; the explicit HTTP_API_OAUTH2_*_URL settings are the fallback when it fails, which is logged as a warning rather than refused.")

// OAuth2Issuer is HTTP_API_OAUTH2_ISSUER.
//
// The issuer this server names in its own OAuth2 documents. Unset, it
// is derived from the externally-visible URL, assuming https.
var OAuth2Issuer = config.DefineString("HTTP_API_OAUTH2_ISSUER", "", config.Startup,
	"The issuer this server names in its own OAuth2 documents. Unset, it is derived from the externally-visible URL, assuming https.")

// OAuth2MaxGrantLifetime is HTTP_API_OAUTH2_MAX_GRANT_LIFETIME.
//
// How long a grant may go unused before the user has to consent again.
// Measured from the grant's last use rather than from consent, so a
// client that refreshes inside the window keeps its grant
// indefinitely, which is what lets an unattended integration keep
// working; an abandoned grant still expires. What bounds a grant in
// active use is the revocation oracles, not this. See "Refresh-grant
// re-authorization" in the operator guide.
var OAuth2MaxGrantLifetime = config.DefineDuration("HTTP_API_OAUTH2_MAX_GRANT_LIFETIME", time.Duration(0) /* 0s */, config.Startup,
	"How long a grant may go unused before the user has to consent again. Measured from the grant's last use rather than from consent, so a client that refreshes inside the window keeps its grant indefinitely, which is what lets an unattended integration keep working; an abandoned grant still expires. What bounds a grant in active use is the revocation oracles, not this. See \"Refresh-grant re-authorization\" in the operator guide.")

// OAuth2RedirectURL is HTTP_API_OAUTH2_REDIRECT_URL.
//
// Where the provider sends the user back to, which has to be a
// redirect URI registered with it. Unset, it is the issuer followed by
// this server's callback path, which is the right answer whenever the
// issuer is how users reach it: /mcp/oauth2/callback on a server whose
// only surface is MCP, /oauth2/callback everywhere else. Both paths
// are served wherever MCP is on, so an authorization started before an
// upgrade still lands; but what has to be registered at the provider
// is the URI this server sends, so a build that gains the web UI needs
// /oauth2/callback added there -- or this set, to keep sending the old
// one.
var OAuth2RedirectURL = config.DefineString("HTTP_API_OAUTH2_REDIRECT_URL", "", config.Startup,
	"Where the provider sends the user back to, which has to be a redirect URI registered with it. Unset, it is the issuer followed by this server's callback path, which is the right answer whenever the issuer is how users reach it: /mcp/oauth2/callback on a server whose only surface is MCP, /oauth2/callback everywhere else. Both paths are served wherever MCP is on, so an authorization started before an upgrade still lands; but what has to be registered at the provider is the URI this server sends, so a build that gains the web UI needs /oauth2/callback added there -- or this set, to keep sending the old one.")

// OAuth2RefreshTokenLifespan is HTTP_API_OAUTH2_REFRESH_TOKEN_LIFESPAN.
//
// How long one refresh token this server issues stays valid. Every
// refresh resets it, which is why HTTP_API_OAUTH2_MAX_GRANT_LIFETIME
// exists: without a cap, a client that keeps refreshing keeps its
// grant forever. See "Refresh-grant re-authorization" in the operator
// guide.
var OAuth2RefreshTokenLifespan = config.DefineDuration("HTTP_API_OAUTH2_REFRESH_TOKEN_LIFESPAN", time.Duration(0) /* 0s */, config.Startup,
	"How long one refresh token this server issues stays valid. Every refresh resets it, which is why HTTP_API_OAUTH2_MAX_GRANT_LIFETIME exists: without a cap, a client that keeps refreshing keeps its grant forever. See \"Refresh-grant re-authorization\" in the operator guide.")

// OAuth2Requirements is HTTP_API_OAUTH2_REQUIREMENTS.
//
// Login policy as a ClassAd expression over the token's claims; a
// login proceeds only where it evaluates true. It is parsed at startup
// and a malformed one stops the server, rather than failing at
// somebody's first login. See "Local identity mapping" in the operator
// guide.
var OAuth2Requirements = config.DefineString("HTTP_API_OAUTH2_REQUIREMENTS", "", config.Startup,
	"Login policy as a ClassAd expression over the token's claims; a login proceeds only where it evaluates true. It is parsed at startup and a malformed one stops the server, rather than failing at somebody's first login. See \"Local identity mapping\" in the operator guide.")

// OAuth2Scopes is HTTP_API_OAUTH2_SCOPES.
//
// Scopes requested at sign-in, space separated as RFC 6749 writes
// them, e.g. "openid profile email org.cilogon.userinfo". Unset uses
// the built-in defaults. Every scope has to be approved for this
// client at the provider: CILogon rejects an unapproved scope
// outright, where it used to ignore it silently, and that refusal
// fails every login. offline_access is added to whatever is set here
// unless HTTP_API_UPSTREAM_REFRESH is off; that one this server adds
// itself, so a refusal of it is absorbed rather than failing logins.
var OAuth2Scopes = config.DefineWords("HTTP_API_OAUTH2_SCOPES", nil, config.Startup,
	"Scopes requested at sign-in, space separated as RFC 6749 writes them, e.g. \"openid profile email org.cilogon.userinfo\". Unset uses the built-in defaults. Every scope has to be approved for this client at the provider: CILogon rejects an unapproved scope outright, where it used to ignore it silently, and that refusal fails every login. offline_access is added to whatever is set here unless HTTP_API_UPSTREAM_REFRESH is off; that one this server adds itself, so a refusal of it is absorbed rather than failing logins.")

// OAuth2TokenURL is HTTP_API_OAUTH2_TOKEN_URL.
//
// The provider's token endpoint, used when discovery is not configured
// or did not answer.
var OAuth2TokenURL = config.DefineString("HTTP_API_OAUTH2_TOKEN_URL", "", config.Startup,
	"The provider's token endpoint, used when discovery is not configured or did not answer.")

// OAuth2UserinfoURL is HTTP_API_OAUTH2_USERINFO_URL.
//
// The provider's userinfo endpoint, used when discovery is not
// configured or did not answer.
var OAuth2UserinfoURL = config.DefineString("HTTP_API_OAUTH2_USERINFO_URL", "", config.Startup,
	"The provider's userinfo endpoint, used when discovery is not configured or did not answer.")

// OAuth2UsernameClaim is HTTP_API_OAUTH2_USERNAME_CLAIM.
//
// Which claim names the account that owns the jobs, e.g. eppn, where
// the provider's subject is not it. Unset uses the subject. See "Local
// identity mapping" in the operator guide.
var OAuth2UsernameClaim = config.DefineString("HTTP_API_OAUTH2_USERNAME_CLAIM", "", config.Startup,
	"Which claim names the account that owns the jobs, e.g. eppn, where the provider's subject is not it. Unset uses the subject. See \"Local identity mapping\" in the operator guide.")

// PingInterval is HTTP_API_PING_INTERVAL.
//
// How often the daemon pings the pool to decide what /readyz reports,
// as a duration with a unit. A bare 0 switches the ping off and is the
// one value exempt from the unit rule, since there is no ambiguity in
// zero. Switching it off is what a deployment holding no local
// HTCondor credential needs: where every request authenticates with a
// forwarded token and the daemon itself has nothing to ping with, the
// ping can only fail and drag /readyz down with it. A value with no
// unit, or one that does not parse, refuses to start rather than being
// guessed at -- 5 could be seconds or minutes.
var PingInterval = config.DefineDuration("HTTP_API_PING_INTERVAL", time.Duration(60000000000) /* 1m */, config.Startup,
	"How often the daemon pings the pool to decide what /readyz reports, as a duration with a unit. A bare 0 switches the ping off and is the one value exempt from the unit rule, since there is no ambiguity in zero. Switching it off is what a deployment holding no local HTCondor credential needs: where every request authenticates with a forwarded token and the daemon itself has nothing to ping with, the ping can only fail and drag /readyz down with it. A value with no unit, or one that does not parse, refuses to start rather than being guessed at -- 5 could be seconds or minutes.")

// ReadTimeout is HTTP_API_READ_TIMEOUT.
//
// How long a request may take to arrive in full, as a Go duration. A
// value that cannot be parsed is logged and the default applies.
var ReadTimeout = config.DefineDuration("HTTP_API_READ_TIMEOUT", time.Duration(30000000000) /* 30s */, config.Startup,
	"How long a request may take to arrive in full, as a Go duration. A value that cannot be parsed is logged and the default applies.")

// Reservations is HTTP_API_RESERVATIONS.
//
// Run the reservations subsystem: the admin API, the admin page and
// the controller. Off by default. With it off the endpoints answer 503
// naming this setting and the admin page is not offered at all. A
// value that is not a boolean refuses to start rather than being read
// as off -- the two ways to misread it are "offered" and "not
// offered", and a daemon that guesses should not come up.
var Reservations = config.DefineBool("HTTP_API_RESERVATIONS", false, config.Startup,
	"Run the reservations subsystem: the admin API, the admin page and the controller. Off by default. With it off the endpoints answer 503 naming this setting and the admin page is not offered at all. A value that is not a boolean refuses to start rather than being read as off -- the two ways to misread it are \"offered\" and \"not offered\", and a daemon that guesses should not come up.")

// ReservationClaims is HTTP_API_RESERVATION_CLAIMS.
//
// Hold always-on reservations as persistent claims (OCUs) on this
// access point's schedd, instead of by pushing machine attributes. Off
// by default and separate from HTTP_API_RESERVATIONS, because it
// changes where a running reservation's capacity comes from and needs
// the schedd to accept the request: this access point has to be a
// queue super user there, or be named in OCU_SUPER_USERS. Scheduled
// reservations are unaffected. Refuses to start on a non-boolean, as
// above.
var ReservationClaims = config.DefineBool("HTTP_API_RESERVATION_CLAIMS", false, config.Startup,
	"Hold always-on reservations as persistent claims (OCUs) on this access point's schedd, instead of by pushing machine attributes. Off by default and separate from HTTP_API_RESERVATIONS, because it changes where a running reservation's capacity comes from and needs the schedd to accept the request: this access point has to be a queue super user there, or be named in OCU_SUPER_USERS. Scheduled reservations are unaffected. Refuses to start on a non-boolean, as above.")

// ReservationEPConstraint is HTTP_API_RESERVATION_EP_CONSTRAINT.
//
// ClassAd expression over the slot ad limiting which execute points
// reservations may draw seats from. Empty means every startd the
// collector reports, which is rarely what a site wants: a class should
// take the teaching nodes rather than whatever the collector happens
// to hold.
var ReservationEPConstraint = config.DefineString("HTTP_API_RESERVATION_EP_CONSTRAINT", "", config.Startup,
	"ClassAd expression over the slot ad limiting which execute points reservations may draw seats from. Empty means every startd the collector reports, which is rarely what a site wants: a class should take the teaching nodes rather than whatever the collector happens to hold.")

// ReservationStampBudget is HTTP_API_RESERVATION_STAMP_BUDGET.
//
// How many cluster ads one stamping pass writes. The write is per
// cluster rather than per job, so the default covers an idle queue far
// larger than most access points have; this exists for a queue that is
// pathological rather than merely large. A value that is not a
// non-negative whole number refuses to start.
var ReservationStampBudget = config.DefineInt("HTTP_API_RESERVATION_STAMP_BUDGET", 0, config.Startup,
	"How many cluster ads one stamping pass writes. The write is per cluster rather than per job, so the default covers an idle queue far larger than most access points have; this exists for a queue that is pathological rather than merely large. A value that is not a non-negative whole number refuses to start.")

// SandboxCacheDir is HTTP_API_SANDBOX_CACHE_DIR.
//
// Where the +jobs file service keeps sandbox output it has already
// fetched. Unset means no cache and every read fetches the sandbox
// again. It has to be real local storage with room for the budget
// below: on tmpfs the cache is memory the kernel cannot reclaim. Its
// contents are discarded at every start and may be deleted by hand
// while the server runs.
var SandboxCacheDir = config.DefineString("HTTP_API_SANDBOX_CACHE_DIR", "", config.Startup,
	"Where the +jobs file service keeps sandbox output it has already fetched. Unset means no cache and every read fetches the sandbox again. It has to be real local storage with room for the budget below: on tmpfs the cache is memory the kernel cannot reclaim. Its contents are discarded at every start and may be deleted by hand while the server runs.")

// SandboxCacheMB is HTTP_API_SANDBOX_CACHE_MB.
//
// That cache's budget in MiB. A negative number disables the cache. A
// value that is not a whole number refuses to start.
var SandboxCacheMB = config.DefineInt("HTTP_API_SANDBOX_CACHE_MB", 0, config.Startup,
	"That cache's budget in MiB. A negative number disables the cache. A value that is not a whole number refuses to start.")

// SandboxMaxGB is HTTP_API_SANDBOX_MAX_GB.
//
// The largest job sandbox the +jobs file service will serve, in GiB. A
// negative number means no limit. Refused the same way as above when
// it is not a whole number.
var SandboxMaxGB = config.DefineInt("HTTP_API_SANDBOX_MAX_GB", 0, config.Startup,
	"The largest job sandbox the +jobs file service will serve, in GiB. A negative number means no limit. Refused the same way as above when it is not a whole number.")

// SpoolBufferDir is HTTP_API_SPOOL_BUFFER_DIR.
//
// Where an archive dropped in the SSH gateway's /submit/, or posted to
// the REST upload endpoints, is buffered while it is unpacked. Unset
// holds it in memory instead, under a much smaller ceiling.
var SpoolBufferDir = config.DefineString("HTTP_API_SPOOL_BUFFER_DIR", "", config.Startup,
	"Where an archive dropped in the SSH gateway's /submit/, or posted to the REST upload endpoints, is buffered while it is unpacked. Unset holds it in memory instead, under a much smaller ceiling.")

// SpoolMaxUploadBytes is HTTP_API_SPOOL_MAX_UPLOAD_BYTES.
//
// The most bytes one upload command may transfer: a PUT or POST to a
// job's input, a redeemed upload URL, or an upload_job_input tool
// call. A cluster-wide upload counts the archive once per proc it
// serves, so a 10 MiB archive spooled to 50 procs is 500 MiB against
// this and not 10. Written as a byte count or a size -- 1000M, 2G,
// 1000MiB all work, and units are powers of 1024. A cluster-wide call
// that cannot fit spools what it can and reports the remainder, so the
// limit costs an extra call rather than failing; a single-proc upload
// over it is refused. A value that is zero, negative or unreadable is
// reported and the default applies: the cap can be raised but not
// turned off, because reading a typo as "no ceiling" would turn one
// bad line into an unbounded fan-out, which is the thing the limit is
// for.
var SpoolMaxUploadBytes = config.DefineBytes("HTTP_API_SPOOL_MAX_UPLOAD_BYTES", 0 /* 0 */, config.Startup,
	"The most bytes one upload command may transfer: a PUT or POST to a job's input, a redeemed upload URL, or an upload_job_input tool call. A cluster-wide upload counts the archive once per proc it serves, so a 10 MiB archive spooled to 50 procs is 500 MiB against this and not 10. Written as a byte count or a size -- 1000M, 2G, 1000MiB all work, and units are powers of 1024. A cluster-wide call that cannot fit spools what it can and reports the remainder, so the limit costs an extra call rather than failing; a single-proc upload over it is refused. A value that is zero, negative or unreadable is reported and the default applies: the cap can be raised but not turned off, because reading a typo as \"no ceiling\" would turn one bad line into an unbounded fan-out, which is the thing the limit is for.")

// SSHCAKeyFile is HTTP_API_SSH_CA_KEY_FILE.
//
// The CA key user certificates are signed with. Unset, one is
// generated and sealed as above. Without a CA key certificates are
// simply unavailable and the device flow still works. Holds or names a
// credential: keep it readable only by the account the server runs as.
var SSHCAKeyFile = config.DefineString("HTTP_API_SSH_CA_KEY_FILE", "", config.Startup,
	"The CA key user certificates are signed with. Unset, one is generated and sealed as above. Without a CA key certificates are simply unavailable and the device flow still works. Holds or names a credential: keep it readable only by the account the server runs as.")

// SSHGatewayAddress is HTTP_API_SSH_GATEWAY_ADDRESS.
//
// Where the SSH gateway listens, e.g. :2222. Unset disables the
// gateway entirely. See "The SSH gateway" in the operator guide.
var SSHGatewayAddress = config.DefineString("HTTP_API_SSH_GATEWAY_ADDRESS", "", config.Startup,
	"Where the SSH gateway listens, e.g. :2222. Unset disables the gateway entirely. See \"The SSH gateway\" in the operator guide.")

// SSHGatewayHost is HTTP_API_SSH_GATEWAY_HOST.
//
// The name or names users actually ssh to, e.g. ap.example.edu,
// separated by commas. It is what the web interface prints as the ssh
// command, and also what the host certificate and the published
// known_hosts line are narrowed to, so list every name and alias in
// use. It cannot be derived: the listen address is usually not what
// anybody types, since a container listening on :2222 sits behind a
// service publishing 22 somewhere else. Unset prints no command and
// falls host verification back to a wildcard pattern.
var SSHGatewayHost = config.DefineString("HTTP_API_SSH_GATEWAY_HOST", "", config.Startup,
	"The name or names users actually ssh to, e.g. ap.example.edu, separated by commas. It is what the web interface prints as the ssh command, and also what the host certificate and the published known_hosts line are narrowed to, so list every name and alias in use. It cannot be derived: the listen address is usually not what anybody types, since a container listening on :2222 sits behind a service publishing 22 somewhere else. Unset prints no command and falls host verification back to a wildcard pattern.")

// SSHGatewayIssuer is HTTP_API_SSH_GATEWAY_ISSUER.
//
// The OAuth2 issuer the gateway's device flow runs against. Unset uses
// this server's own issuer; set it where this process cannot reach its
// own public URL. A login that fails with "could not start the login
// flow" is usually this, left pointing at the default
// http://localhost:8080.
var SSHGatewayIssuer = config.DefineString("HTTP_API_SSH_GATEWAY_ISSUER", "", config.Startup,
	"The OAuth2 issuer the gateway's device flow runs against. Unset uses this server's own issuer; set it where this process cannot reach its own public URL. A login that fails with \"could not start the login flow\" is usually this, left pointing at the default http://localhost:8080.")

// SSHGatewayLockoutDisable is HTTP_API_SSH_GATEWAY_LOCKOUT_DISABLE.
//
// Turn the lockout off. Written true, t, 1, yes or on -- the last two
// are accepted here although HTCondor configuration does not take them
// generally, because this setting always has. It is on by default,
// because the gateway listens on a public SSH port where
// unauthenticated traffic from people who are not users is the
// background noise rather than an exception, and a default of counting
// nothing would mean nobody gets the protection unless they went
// looking for it.
var SSHGatewayLockoutDisable = config.DefineBool("HTTP_API_SSH_GATEWAY_LOCKOUT_DISABLE", false, config.Startup,
	"Turn the lockout off. Written true, t, 1, yes or on -- the last two are accepted here although HTCondor configuration does not take them generally, because this setting always has. It is on by default, because the gateway listens on a public SSH port where unauthenticated traffic from people who are not users is the background noise rather than an exception, and a default of counting nothing would mean nobody gets the protection unless they went looking for it.")

// SSHGatewayLockoutMaxTime is HTTP_API_SSH_GATEWAY_LOCKOUT_MAX_TIME.
//
// Where the doubling stops. Needs a unit, as above.
var SSHGatewayLockoutMaxTime = config.DefineDuration("HTTP_API_SSH_GATEWAY_LOCKOUT_MAX_TIME", time.Duration(0) /* 0s */, config.Startup,
	"Where the doubling stops. Needs a unit, as above.")

// SSHGatewayLockoutNetThreshold is HTTP_API_SSH_GATEWAY_LOCKOUT_NET_THRESHOLD.
//
// The same budget for the network around a host, shared by every
// address in it, so a spray across a handful of addresses is caught
// where each one alone would not be.
var SSHGatewayLockoutNetThreshold = config.DefineInt("HTTP_API_SSH_GATEWAY_LOCKOUT_NET_THRESHOLD", 0, config.Startup,
	"The same budget for the network around a host, shared by every address in it, so a spray across a handful of addresses is caught where each one alone would not be.")

// SSHGatewayLockoutThreshold is HTTP_API_SSH_GATEWAY_LOCKOUT_THRESHOLD.
//
// How many failed logins one host may accumulate inside the window
// before new connections from it are refused. See "Locking out what
// keeps knocking" in the operator guide.
var SSHGatewayLockoutThreshold = config.DefineInt("HTTP_API_SSH_GATEWAY_LOCKOUT_THRESHOLD", 0, config.Startup,
	"How many failed logins one host may accumulate inside the window before new connections from it are refused. See \"Locking out what keeps knocking\" in the operator guide.")

// SSHGatewayLockoutTime is HTTP_API_SSH_GATEWAY_LOCKOUT_TIME.
//
// How long the first lockout of a source lasts. Each subsequent one
// doubles. Needs a unit, as above.
var SSHGatewayLockoutTime = config.DefineDuration("HTTP_API_SSH_GATEWAY_LOCKOUT_TIME", time.Duration(0) /* 0s */, config.Startup,
	"How long the first lockout of a source lasts. Each subsequent one doubles. Needs a unit, as above.")

// SSHGatewayLockoutTrusted is HTTP_API_SSH_GATEWAY_LOCKOUT_TRUSTED.
//
// Addresses and CIDR blocks that are never counted and never locked
// out, separated by commas or spaces or both. Put the office and the
// monitoring host here so that testing the gateway cannot lock you out
// of it, and put a load balancer here if one fronts the port --
// otherwise every client behind it shares one source address and one
// budget. A value that does not parse refuses to start rather than
// being ignored: the setting exists so an operator cannot be shut out
// of their own gateway, and passing over a typo in it would leave them
// believing they are safe. See "Behind a load balancer" in the
// operator guide.
var SSHGatewayLockoutTrusted = config.DefineString("HTTP_API_SSH_GATEWAY_LOCKOUT_TRUSTED", "", config.Startup,
	"Addresses and CIDR blocks that are never counted and never locked out, separated by commas or spaces or both. Put the office and the monitoring host here so that testing the gateway cannot lock you out of it, and put a load balancer here if one fronts the port -- otherwise every client behind it shares one source address and one budget. A value that does not parse refuses to start rather than being ignored: the setting exists so an operator cannot be shut out of their own gateway, and passing over a typo in it would leave them believing they are safe. See \"Behind a load balancer\" in the operator guide.")

// SSHGatewayLockoutWindow is HTTP_API_SSH_GATEWAY_LOCKOUT_WINDOW.
//
// How long a failed login is remembered against its source. Needs a
// unit, and a value without one refuses to start rather than being
// guessed at.
var SSHGatewayLockoutWindow = config.DefineDuration("HTTP_API_SSH_GATEWAY_LOCKOUT_WINDOW", time.Duration(0) /* 0s */, config.Startup,
	"How long a failed login is remembered against its source. Needs a unit, and a value without one refuses to start rather than being guessed at.")

// SSHGatewaySessionCPUs is HTTP_API_SSH_GATEWAY_SESSION_CPUS.
//
// CPUs requested by a session the gateway creates on demand, for a
// caller who connected by name rather than to a job they already have.
var SSHGatewaySessionCPUs = config.DefineInt("HTTP_API_SSH_GATEWAY_SESSION_CPUS", 0, config.Startup,
	"CPUs requested by a session the gateway creates on demand, for a caller who connected by name rather than to a job they already have.")

// SSHGatewaySessionDiskMB is HTTP_API_SSH_GATEWAY_SESSION_DISK_MB.
//
// Disk in MiB for the same session. The default is 8 GiB rather than
// something batch-sized because a VS Code Remote server installs
// itself into the sandbox and dies unpacking it in less -- as a kernel
// out-of-space error deep in a client log, not as anything HTCondor
// reports.
var SSHGatewaySessionDiskMB = config.DefineInt("HTTP_API_SSH_GATEWAY_SESSION_DISK_MB", 0, config.Startup,
	"Disk in MiB for the same session. The default is 8 GiB rather than something batch-sized because a VS Code Remote server installs itself into the sandbox and dies unpacking it in less -- as a kernel out-of-space error deep in a client log, not as anything HTCondor reports.")

// SSHGatewaySessionMemoryMB is HTTP_API_SSH_GATEWAY_SESSION_MEMORY_MB.
//
// Memory in MiB for the same session.
var SSHGatewaySessionMemoryMB = config.DefineInt("HTTP_API_SSH_GATEWAY_SESSION_MEMORY_MB", 0, config.Startup,
	"Memory in MiB for the same session.")

// SSHHostKeyFile is HTTP_API_SSH_HOST_KEY_FILE.
//
// The host key clients pin. Unset, one is generated and kept sealed in
// the application database, which is what most deployments want. Holds
// or names a credential: keep it readable only by the account the
// server runs as.
var SSHHostKeyFile = config.DefineString("HTTP_API_SSH_HOST_KEY_FILE", "", config.Startup,
	"The host key clients pin. Unset, one is generated and kept sealed in the application database, which is what most deployments want. Holds or names a credential: keep it readable only by the account the server runs as.")

// SubmitFileDefaults is HTTP_API_SUBMIT_FILE_DEFAULTS.
//
// Submit-file lines added to every submission, but only where the
// submit file is silent, so a user who sets the same command keeps
// their own value. Written as HTCondor configuration, so several lines
// need that syntax for a multi-line value. Operator-only
// configuration, spliced in as written.
var SubmitFileDefaults = config.DefineString("HTTP_API_SUBMIT_FILE_DEFAULTS", "", config.Startup,
	"Submit-file lines added to every submission, but only where the submit file is silent, so a user who sets the same command keeps their own value. Written as HTCondor configuration, so several lines need that syntax for a multi-line value. Operator-only configuration, spliced in as written.")

// SubmitFileOverrides is HTTP_API_SUBMIT_FILE_OVERRIDES.
//
// Submit-file lines added to every submission that win over whatever
// the submit file says. For requirements that are not the user's to
// opt out of -- an access point that will not have a log written
// outside the home directory, say. Otherwise as above: HTCondor
// configuration syntax, operator-only, spliced in as written.
var SubmitFileOverrides = config.DefineString("HTTP_API_SUBMIT_FILE_OVERRIDES", "", config.Startup,
	"Submit-file lines added to every submission that win over whatever the submit file says. For requirements that are not the user's to opt out of -- an access point that will not have a log written outside the home directory, say. Otherwise as above: HTCondor configuration syntax, operator-only, spliced in as written.")

// TLSCACert is HTTP_API_TLS_CA_CERT.
//
// CA bundle used to verify client certificates. Unset asks for no
// client certificate.
var TLSCACert = config.DefineString("HTTP_API_TLS_CA_CERT", "", config.Startup,
	"CA bundle used to verify client certificates. Unset asks for no client certificate.")

// TLSCert is HTTP_API_TLS_CERT.
//
// PEM certificate for serving HTTPS directly, rather than behind a
// terminating proxy. Needs HTTP_API_TLS_KEY; neither alone enables
// TLS. The restart above is for this SETTING, the path: the file at
// that path is re-read about every five minutes and swapped, so a
// certificate renewed in place takes effect on its own. Put the chain
// here beside the leaf.
var TLSCert = config.DefineString("HTTP_API_TLS_CERT", "", config.Startup,
	"PEM certificate for serving HTTPS directly, rather than behind a terminating proxy. Needs HTTP_API_TLS_KEY; neither alone enables TLS. The restart above is for this SETTING, the path: the file at that path is re-read about every five minutes and swapped, so a certificate renewed in place takes effect on its own. Put the chain here beside the leaf.")

// TLSKey is HTTP_API_TLS_KEY.
//
// Private key for HTTP_API_TLS_CERT, re-read with it. Keep it readable
// only by the account that OWNS it: it is read through the privileged
// path, like the KEK, so a root-owned key works with the daemon
// dropped to condor and does not need widening.
var TLSKey = config.DefineString("HTTP_API_TLS_KEY", "", config.Startup,
	"Private key for HTTP_API_TLS_CERT, re-read with it. Keep it readable only by the account that OWNS it: it is read through the privileged path, like the KEK, so a root-owned key works with the daemon dropped to condor and does not need widening.")

// TokenRetention is HTTP_API_TOKEN_RETENTION.
//
// How long rows for dead OAuth2 tokens are kept before being swept, as
// a duration with a unit, or off (equivalently 0) to keep them
// forever. A bare number is refused rather than read as nanoseconds,
// which is what parsing "90" would otherwise mean and is never what
// anybody intends; so is anything else that does not parse, and the
// server refuses to start rather than sweeping on a figure nobody
// chose. Only rows that are already dead -- revoked or expired -- are
// ever removed, because a live grant is somebody's session whatever
// its age.
var TokenRetention = config.DefineString("HTTP_API_TOKEN_RETENTION", "", config.Startup,
	"How long rows for dead OAuth2 tokens are kept before being swept, as a duration with a unit, or off (equivalently 0) to keep them forever. A bare number is refused rather than read as nanoseconds, which is what parsing \"90\" would otherwise mean and is never what anybody intends; so is anything else that does not parse, and the server refuses to start rather than sweeping on a figure nobody chose. Only rows that are already dead -- revoked or expired -- are ever removed, because a live grant is somebody's session whatever its age.")

// UserHeaderTrustAny is HTTP_API_USER_HEADER_TRUST_ANY.
//
// Accept the user header from any source address. Written true, t, 1,
// yes or on -- the last two are accepted here although HTCondor
// configuration does not take them generally, because this setting
// always has. For demonstration and testing only: with it on, anything
// that can reach this server can claim to be any user, so it undoes
// authentication rather than relaxing it. Off by default, and the
// server logs a warning for as long as it is on. Leave it off anywhere
// a real person has an account.
var UserHeaderTrustAny = config.DefineBool("HTTP_API_USER_HEADER_TRUST_ANY", false, config.Startup,
	"Accept the user header from any source address. Written true, t, 1, yes or on -- the last two are accepted here although HTCondor configuration does not take them generally, because this setting always has. For demonstration and testing only: with it on, anything that can reach this server can claim to be any user, so it undoes authentication rather than relaxing it. Off by default, and the server logs a warning for as long as it is on. Leave it off anywhere a real person has an account.")

// VSCodeImage is HTTP_API_VSCODE_IMAGE.
//
// The container image a VS Code session runs. Any transfer scheme
// works, so a .sif staged on OSDF can be named directly. Set, it wins
// outright and a caller cannot override it; unset, a caller may name
// an image, which grants them nothing new since they can already
// submit a job with any image. Unset otherwise runs a published
// code-server image. See "VS Code sessions" in the operator guide.
var VSCodeImage = config.DefineString("HTTP_API_VSCODE_IMAGE", "", config.Startup,
	"The container image a VS Code session runs. Any transfer scheme works, so a .sif staged on OSDF can be named directly. Set, it wins outright and a caller cannot override it; unset, a caller may name an image, which grants them nothing new since they can already submit a job with any image. Unset otherwise runs a published code-server image. See \"VS Code sessions\" in the operator guide.")

// WebUIAccessGroup is HTTP_API_WEBUI_ACCESS_GROUP.
//
// Obsolete: superseded by HTTP_API_WEBUI_ACCESS_GROUPS. An older
// spelling of HTTP_API_WEBUI_ACCESS_GROUPS, honored where that is
// unset and named as superseded in the startup log.
//
// Deprecated: superseded by HTTP_API_WEBUI_ACCESS_GROUPS.
var WebUIAccessGroup = config.DefineString("HTTP_API_WEBUI_ACCESS_GROUP", "", config.Reconfig,
	"Obsolete: superseded by HTTP_API_WEBUI_ACCESS_GROUPS. An older spelling of HTTP_API_WEBUI_ACCESS_GROUPS, honored where that is unset and named as superseded in the startup log.")

// WebUIAccessGroups is HTTP_API_WEBUI_ACCESS_GROUPS.
//
// Groups whose members may log in to the web interface. Separate from
// the MCP gate because the two answer different questions: somebody
// may be entitled to open this access point's pages without being
// entitled to drive it through an agent, and the reverse. Unset, with
// HTTP_API_WEBUI_ACCESS_USERS unset too, the MCP access gate applies
// instead -- which is what gated the browser before these were
// separate settings. See "Authorization groups" in the operator guide.
var WebUIAccessGroups = config.DefineString("HTTP_API_WEBUI_ACCESS_GROUPS", "", config.Reconfig,
	"Groups whose members may log in to the web interface. Separate from the MCP gate because the two answer different questions: somebody may be entitled to open this access point's pages without being entitled to drive it through an agent, and the reverse. Unset, with HTTP_API_WEBUI_ACCESS_USERS unset too, the MCP access gate applies instead -- which is what gated the browser before these were separate settings. See \"Authorization groups\" in the operator guide.")

// WebUIAccessUsers is HTTP_API_WEBUI_ACCESS_USERS.
//
// Users who may log in to the web interface, named individually rather
// than through a group. Identities are compared as for
// HTTP_API_MCP_ACCESS_USERS. Naming somebody here is enough to make
// the web interface answer to its own gate rather than the MCP one.
// See "Authorization groups" in the operator guide.
var WebUIAccessUsers = config.DefineString("HTTP_API_WEBUI_ACCESS_USERS", "", config.Reconfig,
	"Users who may log in to the web interface, named individually rather than through a group. Identities are compared as for HTTP_API_MCP_ACCESS_USERS. Naming somebody here is enough to make the web interface answer to its own gate rather than the MCP one. See \"Authorization groups\" in the operator guide.")

// WriteTimeout is HTTP_API_WRITE_TIMEOUT.
//
// How long a response may take to write, as a Go duration. This bounds
// a slow client, so it also bounds a deliberately long poll; see the
// note on streaming endpoints before lowering it.
var WriteTimeout = config.DefineDuration("HTTP_API_WRITE_TIMEOUT", time.Duration(30000000000) /* 30s */, config.Startup,
	"How long a response may take to write, as a Go duration. This bounds a slow client, so it also bounds a deliberately long poll; see the note on streaming endpoints before lowering it.")

// ConnectorSettings names the Connector settings of one instance.
//
// An external account users may connect, so a job can read their files
// from it. HTTP_API_CONNECTORS names the connectors offered; each is
// configured by the settings below with its id folded in, e.g.
// HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS for builtin:gdrive.
type ConnectorSettings struct{ id string }

// Connector returns the settings of one Connector instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func Connector(instance string) ConnectorSettings {
	return ConnectorSettings{config.InstanceID(instance)}
}

// ConnectorInstances is the parameter naming every Connector that exists.
var ConnectorInstances = config.DefineCommaList("HTTP_API_CONNECTORS", nil, config.Reconfig,
	"Names the external accounts this access point offers to connect. Each is then configured by the HTTP_API_CONNECTOR_<instance>_* settings.")

// Kind is HTTP_API_CONNECTOR_<instance>_KIND.
//
// Which shipped connector definition to use, for an id that is not
// itself one.
func (s ConnectorSettings) Kind() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_KIND", "", config.Reconfig,
		"Which shipped connector definition to use, for an id that is not itself one.")
}

// ClientID is HTTP_API_CONNECTOR_<instance>_CLIENT_ID.
//
// The OAuth2 client id this access point registered with the provider.
func (s ConnectorSettings) ClientID() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_CLIENT_ID", "", config.Reconfig,
		"The OAuth2 client id this access point registered with the provider.")
}

// ClientSecretFile is HTTP_API_CONNECTOR_<instance>_CLIENT_SECRET_FILE.
//
// File holding the OAuth2 client secret, re-read on each use so a
// rotated secret at an unchanged path needs no restart. A connector
// may instead carry a secret stored through the admin pages; this
// setting wins where both exist. A connector whose secret cannot be
// read is still LISTED, and fails when somebody tries to connect it
// with a 503 naming it -- so check a new connector with the admin test
// rather than waiting for a user to find it. Holds or names a
// credential: keep it readable only by the account the server runs as.
func (s ConnectorSettings) ClientSecretFile() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_CLIENT_SECRET_FILE", "", config.Reconfig,
		"File holding the OAuth2 client secret, re-read on each use so a rotated secret at an unchanged path needs no restart. A connector may instead carry a secret stored through the admin pages; this setting wins where both exist. A connector whose secret cannot be read is still LISTED, and fails when somebody tries to connect it with a 503 naming it -- so check a new connector with the admin test rather than waiting for a user to find it. Holds or names a credential: keep it readable only by the account the server runs as.")
}

// AuthorizationURL is HTTP_API_CONNECTOR_<instance>_AUTHORIZATION_URL.
//
// The provider's authorization endpoint, for a connector that is not a
// shipped one.
func (s ConnectorSettings) AuthorizationURL() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_AUTHORIZATION_URL", "", config.Reconfig,
		"The provider's authorization endpoint, for a connector that is not a shipped one.")
}

// TokenURL is HTTP_API_CONNECTOR_<instance>_TOKEN_URL.
//
// The provider's token endpoint, for a connector that is not a shipped
// one.
func (s ConnectorSettings) TokenURL() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_TOKEN_URL", "", config.Reconfig,
		"The provider's token endpoint, for a connector that is not a shipped one.")
}

// Scopes is HTTP_API_CONNECTOR_<instance>_SCOPES.
//
// The scopes requested when a user connects this account, separated by
// spaces as RFC 6749 delimits them -- the spelling a provider's own
// documentation uses, so it can be pasted. HTTP_API_OAUTH2_SCOPES is
// read the same way.
func (s ConnectorSettings) Scopes() config.ListKnob {
	return config.DefineList("HTTP_API_CONNECTOR_"+s.id+"_SCOPES", nil, config.Reconfig,
		"The scopes requested when a user connects this account, separated by spaces as RFC 6749 delimits them -- the spelling a provider's own documentation uses, so it can be pasted. HTTP_API_OAUTH2_SCOPES is read the same way.")
}

// Groups is HTTP_API_CONNECTOR_<instance>_GROUPS.
//
// Group(s) whose members may connect this account. A connector with
// none may be connected by NOBODY, the same way a site app with no
// groups is not published: an empty value is far more often a mistake
// than a deliberate "nobody". Write "*" for every signed-in user.
// Membership is re-read on each use rather than recorded when the
// account was connected, so removing somebody from the group takes
// their access away.
func (s ConnectorSettings) Groups() config.CommaListKnob {
	return config.DefineCommaList("HTTP_API_CONNECTOR_"+s.id+"_GROUPS", nil, config.Reconfig,
		"Group(s) whose members may connect this account. A connector with none may be connected by NOBODY, the same way a site app with no groups is not published: an empty value is far more often a mistake than a deliberate \"nobody\". Write \"*\" for every signed-in user. Membership is re-read on each use rather than recorded when the account was connected, so removing somebody from the group takes their access away.")
}

// Transfer is HTTP_API_CONNECTOR_<instance>_TRANSFER.
//
// How a job fetches from this account, where the connector supports
// more than one way.
func (s ConnectorSettings) Transfer() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_TRANSFER", "", config.Reconfig,
		"How a job fetches from this account, where the connector supports more than one way.")
}

// Enabled is HTTP_API_CONNECTOR_<instance>_ENABLED.
//
// Whether the connector is offered at all. Default true, so a
// connector is turned off by setting this rather than by removing its
// configuration.
func (s ConnectorSettings) Enabled() config.BoolKnob {
	return config.DefineBool("HTTP_API_CONNECTOR_"+s.id+"_ENABLED", true, config.Reconfig,
		"Whether the connector is offered at all. Default true, so a connector is turned off by setting this rather than by removing its configuration.")
}

// PickerSettings names the Picker settings of one instance.
//
// A file chooser the submit page offers, so a user can pick input
// files out of a connected account instead of uploading them.
// HTTP_API_PICKERS names the choosers that exist; a chooser is
// configured by the settings below with its name folded in, e.g.
// HTTP_API_PICKER_LAB_A_API_KEY for the chooser named lab-a.
type PickerSettings struct{ id string }

// Picker returns the settings of one Picker instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func Picker(instance string) PickerSettings {
	return PickerSettings{config.InstanceID(instance)}
}

// PickerInstances is the parameter naming every Picker that exists.
var PickerInstances = config.DefineCommaList("HTTP_API_PICKERS", nil, config.Reconfig,
	"Names the file choosers this access point offers. Each is then configured by the HTTP_API_PICKER_<instance>_* settings.")

// Provider is HTTP_API_PICKER_<instance>_PROVIDER.
//
// Which shipped chooser to run. Defaults to the chooser's own name, so
// the common case needs one line.
func (s PickerSettings) Provider() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_PROVIDER", "", config.Reconfig,
		"Which shipped chooser to run. Defaults to the chooser's own name, so the common case needs one line.")
}

// Connector is HTTP_API_PICKER_<instance>_CONNECTOR.
//
// The connector whose credential the chooser shows files with. Without
// one the chooser is not offered.
func (s PickerSettings) Connector() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_CONNECTOR", "", config.Reconfig,
		"The connector whose credential the chooser shows files with. Without one the chooser is not offered.")
}

// APIKey is HTTP_API_PICKER_<instance>_API_KEY.
//
// The provider's developer key, where its chooser needs one to run in
// the browser. Holds or names a credential: keep it readable only by
// the account the server runs as.
func (s PickerSettings) APIKey() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_API_KEY", "", config.Reconfig,
		"The provider's developer key, where its chooser needs one to run in the browser. Holds or names a credential: keep it readable only by the account the server runs as.")
}

// AppID is HTTP_API_PICKER_<instance>_APP_ID.
//
// The provider's application id, where its chooser needs one alongside
// the developer key.
func (s PickerSettings) AppID() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_APP_ID", "", config.Reconfig,
		"The provider's application id, where its chooser needs one alongside the developer key.")
}

// Origin is HTTP_API_PICKER_<instance>_ORIGIN.
//
// The origin the chooser is loaded from, when it is not the provider's
// default.
func (s PickerSettings) Origin() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_ORIGIN", "", config.Reconfig,
		"The origin the chooser is loaded from, when it is not the provider's default.")
}

// SiteAppSettings names the SiteApp settings of one instance.
//
// A lab's own web interface, published to the people it belongs to.
// Each bundle in HTTP_API_SITE_APPS_DIR is configured by the settings
// below with its id folded in, e.g. HTTP_API_SITE_APP_CRYOEM_GROUPS
// for the bundle cryoem. GROUPS is what declares an app: a bundle
// nobody has given groups to is not published.
type SiteAppSettings struct{ id string }

// SiteApp returns the settings of one SiteApp instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func SiteApp(instance string) SiteAppSettings {
	return SiteAppSettings{config.InstanceID(instance)}
}

// Groups is HTTP_API_SITE_APP_<instance>_GROUPS.
//
// Group(s) whose members this app is published to. An app with none is
// not published at all -- an empty value is far more often a mistake
// than a deliberate "nobody".
func (s SiteAppSettings) Groups() config.CommaListKnob {
	return config.DefineCommaList("HTTP_API_SITE_APP_"+s.id+"_GROUPS", nil, config.Reconfig,
		"Group(s) whose members this app is published to. An app with none is not published at all -- an empty value is far more often a mistake than a deliberate \"nobody\".")
}

// Home is HTTP_API_SITE_APP_<instance>_HOME.
//
// Whether this app is what its people land on instead of the
// dashboard.
func (s SiteAppSettings) Home() config.BoolKnob {
	return config.DefineBool("HTTP_API_SITE_APP_"+s.id+"_HOME", false, config.Reconfig,
		"Whether this app is what its people land on instead of the dashboard.")
}

// Enabled is HTTP_API_SITE_APP_<instance>_ENABLED.
//
// Whether the app is served. Default true when the setting is present
// at all, so an app is turned off by setting this to false rather than
// by removing its groups.
func (s SiteAppSettings) Enabled() config.BoolKnob {
	return config.DefineBool("HTTP_API_SITE_APP_"+s.id+"_ENABLED", true, config.Reconfig,
		"Whether the app is served. Default true when the setting is present at all, so an app is turned off by setting this to false rather than by removing its groups.")
}

// Name is HTTP_API_SITE_APP_<instance>_NAME.
//
// What the app is called in the navigation, instead of its bundle id.
func (s SiteAppSettings) Name() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_NAME", "", config.Reconfig,
		"What the app is called in the navigation, instead of its bundle id.")
}

// Description is HTTP_API_SITE_APP_<instance>_DESCRIPTION.
//
// One line about the app, shown beside its name.
func (s SiteAppSettings) Description() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_DESCRIPTION", "", config.Reconfig,
		"One line about the app, shown beside its name.")
}

// Icon is HTTP_API_SITE_APP_<instance>_ICON.
//
// The icon shown beside the app's name.
func (s SiteAppSettings) Icon() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_ICON", "", config.Reconfig,
		"The icon shown beside the app's name.")
}

// All is every declared knob -- the settings above and each family's
// enumerating parameter -- for the configuration reference and for tests
// that check the table against the code. A family's per-instance settings
// are not here: they have no name until an instance does.
var All = []config.Knob{
	AnyUserReadGroups.Knob,
	AnyUserReadUsers.Knob,
	AnyUserWriteGroups.Knob,
	AnyUserWriteUsers.Knob,
	BuildDefaultCPUs.Knob,
	BuildDefaultDiskMB.Knob,
	BuildDefaultMemoryMB.Knob,
	BuildExtraSubmit.Knob,
	BuildMaxCPUs.Knob,
	BuildMaxDiskMB.Knob,
	BuildMaxMemoryMB.Knob,
	BuildRequirements.Knob,
	BuildStagingBase.Knob,
	ConnectorOutputSweepInterval.Knob,
	ConnectorPrefix.Knob,
	EnableIDP.Knob,
	IdempotencyKeyCheck.Knob,
	IdempotencyKeyHistoryWindow.Knob,
	IdempotencyKeyRetention.Knob,
	IdempotencyKeyTimeout.Knob,
	IdleTimeout.Knob,
	IDPAccessTokenLifespan.Knob,
	IDPIssuer.Knob,
	IDPRefreshTokenLifespan.Knob,
	InteractiveExtraSubmit.Knob,
	InteractiveJupyter.Knob,
	InteractiveRequirements.Knob,
	InteractiveTerminal.Knob,
	InteractiveVSCode.Knob,
	JobQueueLog.Knob,
	JupyterKernelIdleSec.Knob,
	JupyterMaxLifetimeSec.Knob,
	JupyterReconnectGraceSec.Knob,
	JupyterStartGraceSec.Knob,
	JupyterWorkDir.Knob,
	Listen.Knob,
	LLMAPIKeyFile.Knob,
	LLMAPIURL.Knob,
	LLMAuthScheme.Knob,
	LLMModel.Knob,
	LLMOperatorInstructionsFile.Knob,
	Log.Knob,
	MCPAccessGroup.Knob,
	MCPAccessGroups.Knob,
	MCPAccessUsers.Knob,
	MCPAdminGroup.Knob,
	MCPBaseURL.Knob,
	MCPConfirmRemoveOver.Knob,
	MCPConfirmSubmitOver.Knob,
	MCPDisabledTools.Knob,
	MCPListenAddr.Knob,
	MCPMaxRequestDuration.Knob,
	MCPReadGroup.Knob,
	MCPReadGroups.Knob,
	MCPReadOnly.Knob,
	MCPReadUsers.Knob,
	MCPRedactDisabledRules.Knob,
	MCPRedactSecrets.Knob,
	MCPSkillsDir.Knob,
	MCPSkillsReloadInterval.Knob,
	MCPSuperuserGroup.Knob,
	MCPWatchMaxWait.Knob,
	MCPWriteGroup.Knob,
	MCPWriteGroups.Knob,
	MCPWriteUsers.Knob,
	OAuth2AccessTokenLifespan.Knob,
	OAuth2AuthURL.Knob,
	OAuth2ClientID.Knob,
	OAuth2ClientSecretFile.Knob,
	OAuth2DBPath.Knob,
	OAuth2GroupsClaim.Knob,
	OAuth2IDP.Knob,
	OAuth2Issuer.Knob,
	OAuth2MaxGrantLifetime.Knob,
	OAuth2RedirectURL.Knob,
	OAuth2RefreshTokenLifespan.Knob,
	OAuth2Requirements.Knob,
	OAuth2Scopes.Knob,
	OAuth2TokenURL.Knob,
	OAuth2UserinfoURL.Knob,
	OAuth2UsernameClaim.Knob,
	PingInterval.Knob,
	ReadTimeout.Knob,
	Reservations.Knob,
	ReservationClaims.Knob,
	ReservationEPConstraint.Knob,
	ReservationStampBudget.Knob,
	SandboxCacheDir.Knob,
	SandboxCacheMB.Knob,
	SandboxMaxGB.Knob,
	SpoolBufferDir.Knob,
	SpoolMaxUploadBytes.Knob,
	SSHCAKeyFile.Knob,
	SSHGatewayAddress.Knob,
	SSHGatewayHost.Knob,
	SSHGatewayIssuer.Knob,
	SSHGatewayLockoutDisable.Knob,
	SSHGatewayLockoutMaxTime.Knob,
	SSHGatewayLockoutNetThreshold.Knob,
	SSHGatewayLockoutThreshold.Knob,
	SSHGatewayLockoutTime.Knob,
	SSHGatewayLockoutTrusted.Knob,
	SSHGatewayLockoutWindow.Knob,
	SSHGatewaySessionCPUs.Knob,
	SSHGatewaySessionDiskMB.Knob,
	SSHGatewaySessionMemoryMB.Knob,
	SSHHostKeyFile.Knob,
	SubmitFileDefaults.Knob,
	SubmitFileOverrides.Knob,
	TLSCACert.Knob,
	TLSCert.Knob,
	TLSKey.Knob,
	TokenRetention.Knob,
	UserHeaderTrustAny.Knob,
	VSCodeImage.Knob,
	WebUIAccessGroup.Knob,
	WebUIAccessGroups.Knob,
	WebUIAccessUsers.Knob,
	WriteTimeout.Knob,
	ConnectorInstances.Knob,
	PickerInstances.Knob,
}

// Obsolete maps each obsolete setting to the setting that replaces
// it, or to the empty string where nothing single replaces it. It is
// what the reference leaves out and what a test checks stays out of
// the documentation; a daemon can also use it to report a setting an
// operator still has in their configuration.
var Obsolete = map[string]string{
	"HTTP_API_LISTEN":              "HTTP_API_LISTEN_ADDR",
	"HTTP_API_MCP_ACCESS_GROUP":    "HTTP_API_MCP_ACCESS_GROUPS",
	"HTTP_API_MCP_ADMIN_GROUP":     "HTTP_API_ANYUSER_READ_GROUPS",
	"HTTP_API_MCP_READ_GROUP":      "HTTP_API_MCP_READ_GROUPS",
	"HTTP_API_MCP_SUPERUSER_GROUP": "HTTP_API_ANYUSER_WRITE_GROUPS",
	"HTTP_API_MCP_WRITE_GROUP":     "HTTP_API_MCP_WRITE_GROUPS",
	"HTTP_API_WEBUI_ACCESS_GROUP":  "HTTP_API_WEBUI_ACCESS_GROUPS",
}
