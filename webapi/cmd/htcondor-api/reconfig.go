package main

import (
	"slices"
	"sort"

	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/knobs"
)

// Reconfigure: what a running htcondor-api does with SIGHUP (or condor_reconfig,
// which arrives as DC_RECONFIG and runs the same path).
//
// The daemon framework reloads the configuration file and re-applies log levels
// on its own. Everything else about this server is read once at startup and
// copied into the running objects, so until a parameter is listed here a change
// to it either does nothing or is not noticed at all. The second case is the
// one that costs an operator an afternoon: reconfigure appeared to succeed, and
// the setting they edited simply never took.
//
// So every parameter in this table is one the daemon has an answer for. Those
// with an apply function are installed on the running server; those without are
// reported as needing a restart, naming the parameter. Making a parameter
// dynamic means writing its setter (see httpserver/reconfig.go) and moving it
// up into the dynamic group -- the reporting half then takes care of itself.
type reconfigParam struct {
	// name is the bare parameter name. Lookup goes through config.Get, so
	// the daemon's subsystem and -local-name scoping applies, exactly as it
	// did when the value was first read at startup.
	//
	// Prefer fromKnob for a setting the knob table declares: the name
	// then comes from the declaration, and TestReconfigScopesMatchTheKnobs
	// checks that this table and the declared reload scope agree.
	name string
	// apply installs a new value on the running server. Nil means the
	// parameter is read only at startup and a change requires a restart.
	apply func(reconfigTarget, string)
}

// fromKnob makes a restart-required entry for a declared setting: the
// daemon reads it once at startup, so a reconfigure reports it rather
// than applying it.
func fromKnob(k config.Knob) reconfigParam {
	return reconfigParam{name: k.Name()}
}

// A dynamic entry for a declared setting is written out rather than
// built by fromKnob, because it carries an apply as well as the name:
// see the two MCP group entries below. TestReconfigScopesMatchTheKnobs
// is what says so when one is declared scope: reconfig and listed
// without an apply.

// reconfigTarget is the running server a reconfigure updates. It is an
// interface, not *httpserver.Server, so the table and its bookkeeping can be
// exercised without standing up an HTTP server; *httpserver.Server satisfies it
// through the setters in httpserver/reconfig.go.
type reconfigTarget interface {
	SetMCPInstructions(string)
	SetMCPAccessGroups(string)
	SetMCPReadGroups(string)
	SetMCPWriteGroups(string)
	SetMCPAdminGroups(string)
	SetMCPSuperuserGroups(string)
	SetWebUIAccessGroups(string)
	SetWebUIAppOnlyGroups(string)
	SetWebUIAdminGroups(string)
	SetWebUISiteName(string)
	SetWebUISiteTagline(string)
	SetWebUIFooterLogos(string)
	SetWebUIBrandingDir(string)
	SetWebUICustomCSS(string)
	SetWebUIFavicon(string)
	SetWebUIPrivacyFile(string)
	SetWebUIPrivacyURL(string)
	SetWebUITermsFile(string)
	SetWebUITermsURL(string)
	SetSuperuserGroups(string)
	SetProjectLeadsFile(string)
	SetProjectLeadsGroup(string)
	SetMCPSkillsDir(string)
	// SetSiteAppPolicy takes the configuration itself rather than one
	// value: an app's policy is several parameters, and which apps
	// exist is not known until they are read.
	SetSiteAppPolicy(*config.Config)
	// SetConnectorConfig takes the configuration itself for the same
	// reason SetSiteAppPolicy does: a diff cannot see a connector that
	// was not there before.
	SetConnectorConfig(*config.Config)
	SetMCPDisabledTools(string)
	SetMCPReadOnly(string)
	SetMCPConfirmSubmitOver(string)
	SetMCPConfirmRemoveOver(string)
	SetInteractiveJupyter(string)
	SetInteractiveVSCode(string)
	SetInteractiveTerminal(string)
	SetMCPRedactSecrets(string)
	SetMCPRedactDisabledRules(string)
	SetChatInstructions(string)
	// ReloadAPAssignment re-reads the access point assignment's files
	// (the mapfile, the REST token) and drops cached answers.
	ReloadAPAssignment()
}

// reconfigParams is the table described above. It is deliberately a list of
// parameters this daemon actually consumes rather than every key in the
// configuration: a warning about a pool-wide knob htcondor-api never reads
// would be noise, and noise is what teaches operators to ignore warnings.
var reconfigParams = []reconfigParam{
	// --- Applied on reconfigure ---

	// Deployment-specific MCP guidance. Pure text handed to agents in the
	// initialize response, with nothing built from it, which is what makes
	// it safe to swap under live traffic.
	{
		name: "MCP_INSTRUCTIONS",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPInstructions(v)
		},
	},

	// Which MCP tools this site offers. The catalogue is rebuilt per
	// call and the check runs on every dispatch, so nothing is
	// constructed from this and a change takes effect on the next
	// request -- including for sessions already connected, which is the
	// point: a tool withdrawn because policy changed should stop working
	// without waiting for agents to reconnect.
	{
		name: "HTTP_API_MCP_DISABLED_TOOLS",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPDisabledTools(v)
		},
	},

	// Whether this access point serves MCP read-only. Dynamic for the
	// same reason, and for one more: it is the setting an operator
	// reaches for when an agent has just done something they did not
	// expect, which is the worst possible moment to be told to restart
	// the daemon.
	{
		name: "HTTP_API_MCP_READ_ONLY",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPReadOnly(v)
		},
	},

	// The job counts above which the MCP submit and remove tools ask a
	// person to confirm. Dynamic for the same reason as the setting
	// above, and reachable the same way: an operator who has just seen
	// an agent act at an unexpected scale wants the gate up for the
	// sessions already connected, not at the next restart.
	{
		name: "HTTP_API_MCP_CONFIRM_SUBMIT_OVER",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPConfirmSubmitOver(v)
		},
	},
	{
		name: "HTTP_API_MCP_CONFIRM_REMOVE_OVER",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPConfirmRemoveOver(v)
		},
	},

	// Whether anything may reach into a running job. NOT dynamic: the
	// SSH gateway's listener is built at startup, and a setting that
	// closed the tool surface while leaving the port open would be the
	// worst of both. An operator turning this off restarts the daemon,
	// and is told so rather than left to discover it.
	{name: "HTTP_API_SSH_TO_JOB"},

	// Which built-in interactive applications this access point offers.
	// Dynamic because nothing is built from them: each is a check made
	// when a session is created. Restarting the daemon to stop new
	// notebooks being made would drop every notebook already running,
	// which is precisely what these settings take care not to do.
	{
		name:  "HTTP_API_INTERACTIVE_JUPYTER",
		apply: func(s reconfigTarget, v string) { s.SetInteractiveJupyter(v) },
	},
	{
		name:  "HTTP_API_INTERACTIVE_VSCODE",
		apply: func(s reconfigTarget, v string) { s.SetInteractiveVSCode(v) },
	},
	{
		name:  "HTTP_API_INTERACTIVE_TERMINAL",
		apply: func(s reconfigTarget, v string) { s.SetInteractiveTerminal(v) },
	},

	// Credential masking on MCP tool results. Both settings only decide
	// which rules a result is run through, and the rule set is rebuilt on
	// the reconfigure goroutine and swapped atomically, so a change takes
	// effect on the next tool call. Dynamic for the same reason the
	// disabled-tool list is: a site that discovers its output is leaking
	// a credential, or that a rule is eating something it needs, should
	// not have to restart the daemon to say so.
	{
		name: "HTTP_API_MCP_REDACT_SECRETS",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPRedactSecrets(v)
		},
	},
	{
		name: "HTTP_API_MCP_REDACT_DISABLED_RULES",
		apply: func(s reconfigTarget, v string) {
			s.SetMCPRedactDisabledRules(v)
		},
	},

	// Authorization group lists. Each is only a membership test on a
	// request -- nothing is constructed from one -- so they can be swapped
	// under live traffic. These are also the settings most often wrong on
	// a first deployment, where the alternative to a reconfigure is
	// restarting the daemon to find out whether the fix was right.
	//
	// Sessions already granted keep their grant until they refresh, at
	// which point the reauthorization path re-runs the policy.
	{
		name:  "HTTP_API_MCP_ACCESS_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetMCPAccessGroups(v) },
	},
	{
		name:  "HTTP_API_MCP_READ_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetMCPReadGroups(v) },
	},
	{
		name:  "HTTP_API_MCP_WRITE_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetMCPWriteGroups(v) },
	},
	// Declared, and obsolete: the name comes from the table so the two
	// cannot drift, and staticcheck reports the read for the reason it
	// does at the startup read in main.go.
	{
		//nolint:staticcheck // SA1019: honoring a setting an operator may still have set
		name:  knobs.MCPAdminGroup.Name(),
		apply: func(s reconfigTarget, v string) { s.SetMCPAdminGroups(v) },
	},
	{
		//nolint:staticcheck // SA1019: honoring a setting an operator may still have set
		name:  knobs.MCPSuperuserGroup.Name(),
		apply: func(s reconfigTarget, v string) { s.SetMCPSuperuserGroups(v) },
	},
	// Reloaded unconditionally in reconfigure() as well; this entry is what
	// handles the path being changed or cleared.
	//
	// A running daemon also polls this directory on its own
	// (HTTP_API_MCP_SKILLS_RELOAD_INTERVAL), so a checkout updated in place
	// no longer needs a reconfigure at all. What a reconfigure adds is
	// immediacy and an unconditional re-read: the poll skips files whose
	// size and modification time are unchanged, and this does not.
	{
		name:  "HTTP_API_MCP_SKILLS_DIR",
		apply: func(s reconfigTarget, v string) { s.SetMCPSkillsDir(v) },
	},
	// The web chat assistant's half of MCP_INSTRUCTIONS: the operator's
	// own rules, handed to the model on every turn. Re-read on every
	// reconfigure rather than only when the path changes, because the
	// usual reason to reload is that the file's contents were edited --
	// the same reasoning as HTTP_API_MCP_SKILLS_DIR above.
	{
		name:  "HTTP_API_LLM_OPERATOR_INSTRUCTIONS_FILE",
		apply: func(s reconfigTarget, v string) { s.SetChatInstructions(v) },
	},
	{
		name:  "HTTP_API_WEBUI_ACCESS_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetWebUIAccessGroups(v) },
	},
	{
		name:  "HTTP_API_WEBUI_APP_ONLY_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetWebUIAppOnlyGroups(v) },
	},
	{
		name:  "HTTP_API_WEBUI_ADMIN_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetWebUIAdminGroups(v) },
	},

	// Web UI branding. Read when a page is served and nothing built
	// from any of it, so an operator trying their logos and colours out
	// sees each attempt on the next page load rather than on the next
	// restart. The file-valued ones are re-checked, so a path that was
	// wrong at startup starts working here.
	{
		name:  "HTTP_API_WEBUI_SITE_NAME",
		apply: func(s reconfigTarget, v string) { s.SetWebUISiteName(v) },
	},
	{
		name:  "HTTP_API_WEBUI_SITE_TAGLINE",
		apply: func(s reconfigTarget, v string) { s.SetWebUISiteTagline(v) },
	},
	{
		name:  "HTTP_API_WEBUI_FOOTER_LOGOS",
		apply: func(s reconfigTarget, v string) { s.SetWebUIFooterLogos(v) },
	},
	{
		name:  "HTTP_API_WEBUI_BRANDING_DIR",
		apply: func(s reconfigTarget, v string) { s.SetWebUIBrandingDir(v) },
	},
	{
		name:  "HTTP_API_WEBUI_CUSTOM_CSS",
		apply: func(s reconfigTarget, v string) { s.SetWebUICustomCSS(v) },
	},
	{
		name:  "HTTP_API_WEBUI_FAVICON",
		apply: func(s reconfigTarget, v string) { s.SetWebUIFavicon(v) },
	},
	// The site's policy pages. Reloadable on the same terms: the
	// document is read when the page is served, so an operator
	// correcting a sentence does not need a restart -- and neither
	// does one turning a page on for the first time.
	{
		name:  "HTTP_API_WEBUI_PRIVACY_FILE",
		apply: func(s reconfigTarget, v string) { s.SetWebUIPrivacyFile(v) },
	},
	{
		name:  "HTTP_API_WEBUI_PRIVACY_URL",
		apply: func(s reconfigTarget, v string) { s.SetWebUIPrivacyURL(v) },
	},
	{
		name:  "HTTP_API_WEBUI_TERMS_FILE",
		apply: func(s reconfigTarget, v string) { s.SetWebUITermsFile(v) },
	},
	{
		name:  "HTTP_API_WEBUI_TERMS_URL",
		apply: func(s reconfigTarget, v string) { s.SetWebUITermsURL(v) },
	},
	// Membership only: superuser mode is built at startup, so this cannot
	// switch it on in a running daemon. Emptying it does switch it off.
	{
		name:  "HTTP_API_SUPERUSER_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetSuperuserGroups(v) },
	},
	// Project leads. Membership tests only, like the groups above, with
	// the same limit: they cannot switch superuser mode on in a daemon
	// that started without it. The file is also re-read unconditionally
	// in reconfigure(), since editing it is the usual reason to send one.
	{
		name:  "HTTP_API_PROJECT_LEADS_FILE",
		apply: func(s reconfigTarget, v string) { s.SetProjectLeadsFile(v) },
	},
	{
		name:  "HTTP_API_PROJECT_LEADS_GROUP",
		apply: func(s reconfigTarget, v string) { s.SetProjectLeadsGroup(v) },
	},

	// --- Restart required (read once at startup) ---

	// The server's own identity. Beyond the links it builds, this is the
	// OAuth2 and IDP issuer: it is the `iss` of every token already issued
	// and the origin of the redirect URIs registered clients hold, so it
	// cannot be swapped under live sessions without invalidating them.
	{name: "HTTP_API_BASE_URL"},
	fromKnob(knobs.MCPBaseURL.Knob),

	// Where this daemon logs, opened once when the logger is built, and
	// the schedd log it tails for the job change stream, opened once
	// when the stream starts.
	fromKnob(knobs.Log.Knob),
	fromKnob(knobs.JobQueueLog.Knob),

	// Read when their one consumer is built: the staging directory for
	// JupyterLab submissions, the credd service-name prefix connected
	// accounts are stored under, and how often the pool is pinged for
	// what /readyz reports.
	fromKnob(knobs.JupyterWorkDir.Knob),
	fromKnob(knobs.ConnectorPrefix.Knob),
	fromKnob(knobs.PingInterval.Knob),

	// Listener and credentials, all bound or opened during startup.
	{name: "HTTP_API_LISTEN_ADDR"},
	// The separate MCP listener, bound at the same time.
	fromKnob(knobs.MCPListenAddr.Knob),
	// The older spelling of the same thing, listed so a change to it is
	// reported as needing a restart rather than silently ignored. An
	// operator still using the alias is the one most likely to be
	// surprised.
	//nolint:staticcheck // SA1019: honoring a setting an operator may still have set
	fromKnob(knobs.Listen.Knob),
	{name: "HTTP_API_SIGNING_KEY"},
	{name: "HTTP_API_KEK_FILE"},
	fromKnob(knobs.TLSCert.Knob),
	fromKnob(knobs.TLSKey.Knob),
	// Read at startup like the pair above and, until now, listed
	// nowhere: a change to it did nothing and nothing said so.
	fromKnob(knobs.TLSCACert.Knob),

	// The built-in identity provider, stood up once at startup: turning
	// it on or off, and the issuer and token lifetimes it stamps into
	// everything it has already signed.
	fromKnob(knobs.EnableIDP.Knob),
	fromKnob(knobs.IDPIssuer.Knob),
	fromKnob(knobs.IDPAccessTokenLifespan.Knob),
	fromKnob(knobs.IDPRefreshTokenLifespan.Knob),

	// The same for the OAuth2 authorization server this one is. The
	// lifespans are handed to the provider when it is built, so a
	// change reaches nothing already issued and nothing issued next
	// either until a restart.
	fromKnob(knobs.OAuth2AccessTokenLifespan.Knob),
	fromKnob(knobs.OAuth2RefreshTokenLifespan.Knob),
	fromKnob(knobs.OAuth2MaxGrantLifetime.Knob),

	// The retention sweep's window, read when the sweeper is started.
	fromKnob(knobs.TokenRetention.Knob),

	// Whether the user header is trusted from anywhere, decided when
	// the handler is built. Listed so that turning it off in the
	// configuration and reconfiguring does not look like it worked.
	fromKnob(knobs.UserHeaderTrustAny.Knob),

	// The HTTP server's timeouts, captured when it was built.
	fromKnob(knobs.ReadTimeout.Knob),
	fromKnob(knobs.WriteTimeout.Knob),
	fromKnob(knobs.IdleTimeout.Knob),

	// Where an upload buffers. Read once when the handler is built.
	fromKnob(knobs.SpoolBufferDir.Knob),

	// Whether MCP serves OAuth2 at all. It decides which routes the mux
	// is built with and whether the provider is constructed, both of
	// which happen once.
	{name: "HTTP_API_MCP_OAUTH2"},

	// OAuth2 / IDP wiring, captured when the provider was constructed.
	fromKnob(knobs.OAuth2Issuer.Knob),
	fromKnob(knobs.OAuth2IDP.Knob),
	fromKnob(knobs.OAuth2ClientID.Knob),
	// Discovery's fallbacks and the redirect, read with the rest of
	// the provider wiring and previously listed nowhere.
	fromKnob(knobs.OAuth2AuthURL.Knob),
	fromKnob(knobs.OAuth2TokenURL.Knob),
	fromKnob(knobs.OAuth2UserinfoURL.Knob),
	fromKnob(knobs.OAuth2RedirectURL.Knob),
	fromKnob(knobs.OAuth2ClientSecretFile.Knob),
	fromKnob(knobs.OAuth2Scopes.Knob),
	fromKnob(knobs.OAuth2UsernameClaim.Knob),
	fromKnob(knobs.OAuth2GroupsClaim.Knob),
	fromKnob(knobs.OAuth2Requirements.Knob),
	{name: "HTTP_API_IDENTITY_MAP_STRIP_DOMAIN"},

	// The skills poll's ticker is started once, when the MCP server is
	// built. Changing how often it fires needs a restart; changing what it
	// reads does not (see HTTP_API_MCP_SKILLS_DIR above).
	{name: "HTTP_API_MCP_SKILLS_RELOAD_INTERVAL"},

	// The connector output sweep's ticker is likewise started once. Which
	// connectors exist, and their policy, do reload; how often finished
	// jobs' results are appended does not.
	fromKnob(knobs.ConnectorOutputSweepInterval.Knob),

	// Authorization groups. Worth making dynamic later -- they are consulted
	// per request -- but some are also folded into policy objects at
	// startup, so they need their setters before they can move up.
	{name: "MCP_ADMIN_USERS"},

	// API-key entitlement policy, read per request but off the Handler's
	// own fields, which are set once at construction.
	{name: "HTTP_API_API_KEY_ENTITLEMENT_TTL"},
	{name: "HTTP_API_SERVICE_KEY_MAX_LIFETIME"},

	// Pool query policy, compiled into the attribute allowlist at startup.
	{name: "HTTP_API_POOL_QUERY_IDENTITY"},
	{name: "HTTP_API_POOL_QUERY_EXTRA_ATTRS"},

	// Submit-time policy, compiled into the submit policy at startup.
	fromKnob(knobs.SubmitFileDefaults.Knob),
	fromKnob(knobs.SubmitFileOverrides.Knob),

	// Idempotency-key enforcement, folded into the Guard at startup.
	// Restart-only on purpose rather than for want of a setter: the
	// Guard holds the claims, and rebuilding it would drop every key
	// claimed in the retention window -- turning a reconfig into a
	// window in which a retry is accepted twice.
	fromKnob(knobs.IdempotencyKeyCheck.Knob),
	fromKnob(knobs.IdempotencyKeyRetention.Knob),
	fromKnob(knobs.IdempotencyKeyTimeout.Knob),
	fromKnob(knobs.IdempotencyKeyHistoryWindow.Knob),
	{name: "HTTP_API_INTERACTIVE_REQUIREMENTS"},
	{name: "HTTP_API_DAGMAN_PATH"},
	{name: "HTTP_API_DAGMAN_ENVIRONMENT"},
	fromKnob(knobs.InteractiveExtraSubmit.Knob),
	fromKnob(knobs.BuildExtraSubmit.Knob),
	fromKnob(knobs.BuildRequirements.Knob),
	fromKnob(knobs.BuildStagingBase.Knob),
	{name: "HTTP_API_VSCODE_IMAGE"},
	fromKnob(knobs.BuildDefaultCPUs.Knob),
	fromKnob(knobs.BuildDefaultMemoryMB.Knob),
	fromKnob(knobs.BuildDefaultDiskMB.Knob),
	fromKnob(knobs.BuildMaxCPUs.Knob),
	fromKnob(knobs.BuildMaxMemoryMB.Knob),
	fromKnob(knobs.BuildMaxDiskMB.Knob),

	// Tool-usage statistics. The flush ticker is started once and the
	// collector is registered once, both when the handler is built, so
	// every one of these is read at startup and nowhere else.
	{name: "HTTP_API_TOOL_STATS_FLUSH_INTERVAL"},
	{name: "HTTP_API_TOOL_STATS_MAX_LABEL_VALUES"},
	{name: "HTTP_API_TOOL_STATS_MAX_SERIES"},
	{name: "HTTP_API_TOOL_STATS_OMIT_USER"},

	// The utilization page's ceiling on jobs per analysis, read when the
	// handler is built.
	{name: "HTTP_API_UTILIZATION_MAX_JOBS"},

	// The ceiling on one upload command, copied into the handler and
	// into the MCP server it builds, both at startup.
	fromKnob(knobs.SpoolMaxUploadBytes.Knob),

	// Placement onboarding, read when the handler is built.
	{name: "HTTP_API_PLACEMENT_IDENTITY_CLAIMS"},
	{name: "HTTP_API_PLACEMENT_DISPLAY_NAME_CLAIMS"},
	{name: "HTTP_API_PLACEMENT_DEVICE_CLIENTS"},
	{name: "HTTP_API_PLACEMENT_SCHEDD_HOST"},
	{name: "HTTP_API_PLACEMENT_COLLECTOR_HOST"},
	{name: "HTTP_API_PLACEMENT_PREFILL_DOMAINS"},
	{name: "HTTP_API_PLACEMENT_REQUEST_RETENTION"},
	// Which placementd discovery accepts, decided when the handler is
	// built.
	{name: "HTTP_API_PLACEMENTD_NAME"},
	{name: "PLACEMENTD_HOST"},

	// Mirror routing, resolved into a discovery locator at startup.
	{name: "HTTP_API_DBMIRROR_NAME"},
	{name: "HTTP_API_DBMIRROR_ADDRESS"},
	{name: "HTTP_API_DBMIRROR_REQUIRED"},
	{name: "HTTP_API_DBMIRROR_MAX_LIMIT"},
	{name: "HTTP_API_DBMIRROR_MAX_PROJECTED_LIMIT"},

	// Multi-AP mode: which mode the server runs in, and what it reads
	// from, are decided when the handler is built.
	{name: "HTTP_API_SCHEDD_CONSTRAINT"},
	{name: "HTTP_API_HUB_NAME"},
	{name: "HTTP_API_HUB_ADDRESS"},
	{name: "HTTP_API_MULTI_AP_STALE"},
	{name: "HTTP_API_JOB_ID_CODEC"},
	{name: "HTTP_API_BULK_FRESH_SECONDS"},
	{name: "HTTP_API_HUB_SWITCH_SECONDS"},
	{name: "HTTP_API_HUB_SWITCH_MARGIN"},
	{name: "HTTP_API_HUB_FAIL_AFTER"},
	{name: "HTTP_API_HUB_SETTLE_SECONDS"},
	// The assignment chain is built at startup; the mapfile's and token
	// file's contents are re-read on every reconfigure (see reconfigure).
	{name: "HTTP_API_AP_ASSIGNMENT"},
	{name: "HTTP_API_AP_ASSIGNMENT_DEFAULT"},
	{name: "HTTP_API_AP_ASSIGNMENT_MAPFILE"},
	{name: "HTTP_API_AP_ASSIGNMENT_REST_URL"},
	{name: "HTTP_API_AP_ASSIGNMENT_REST_TOKEN_FILE"},
	{name: "HTTP_API_AP_ASSIGNMENT_STALE_SECONDS"},
	// The site app library and its rescan timer are built at startup,
	// so moving the directory or changing the interval needs a
	// restart. The POLICY in HTTP_API_SITE_APP_* is not here: it is
	// not one parameter per app, and a new app brings parameters this
	// table could not have been watching, so it is re-read whole on
	// every reconfigure (see reconfigure).
	{name: "HTTP_API_SITE_APPS_DIR"},
	{name: "HTTP_API_SITE_APPS_RELOAD_INTERVAL"},
}

// reconfigWatcher remembers what each known parameter was set to, so a
// reconfigure can report what actually changed rather than re-applying
// everything blindly.
type reconfigWatcher struct {
	srv    reconfigTarget
	logger *logging.Logger
	params []reconfigParam
	// prev is the value each parameter had the last time it was observed:
	// at startup, then after each reconfigure.
	prev map[string]string
}

// newReconfigWatcher snapshots the current value of every known parameter.
// cfg must be the configuration the server was built from, so the first
// reconfigure compares against what is actually running.
func newReconfigWatcher(cfg *config.Config, srv reconfigTarget, logger *logging.Logger) *reconfigWatcher {
	w := &reconfigWatcher{srv: srv, logger: logger, params: reconfigParams, prev: map[string]string{}}
	for _, p := range w.params {
		w.prev[p.name] = configValue(cfg, p.name)
	}
	return w
}

// reconfigure applies the parameters that can be applied and reports the ones
// that cannot. It is registered with daemon.OnReconfig, so it runs on the
// daemon's signal goroutine while requests are being served.
func (w *reconfigWatcher) reconfigure(cfg *config.Config) {
	applied, needRestart := w.diff(cfg)

	// The skill library is reloaded from disk on every reconfigure, not
	// only when HTTP_API_MCP_SKILLS_DIR changes. The usual reason to
	// reload is that the checkout the path points at was updated -- a git
	// pull, a new deployment of the site's documentation -- which a diff
	// of the setting itself cannot see. This is the only parameter here
	// whose meaning lives outside the configuration file.
	if dir := configValue(cfg, "HTTP_API_MCP_SKILLS_DIR"); dir != "" {
		w.srv.SetMCPSkillsDir(dir)
		if !slices.Contains(applied, "HTTP_API_MCP_SKILLS_DIR") {
			applied = append(applied, "HTTP_API_MCP_SKILLS_DIR")
		}
	}

	// Site app policy is spread over a parameter per app per setting,
	// and publishing a new app adds names nothing was watching. A diff
	// of any one of them cannot see that, so the whole lot is re-read
	// and the bundle directory rescanned on every reconfigure. This is
	// also how an operator makes a policy edit take effect now rather
	// than at the end of the reload interval.
	// Connector definitions are spread over a parameter per connector
	// per setting, and declaring a new one adds names nothing was
	// watching, so the whole lot is re-read on every reconfigure. Same
	// shape as site app policy directly above, and for the same reason.
	w.srv.SetConnectorConfig(cfg)

	w.srv.SetSiteAppPolicy(cfg)

	// Same for the project leads file: its contents live outside the
	// configuration, so a diff of the path cannot see an edit to it.
	if path := configValue(cfg, "HTTP_API_PROJECT_LEADS_FILE"); path != "" {
		if !slices.Contains(applied, "HTTP_API_PROJECT_LEADS_FILE") {
			w.srv.SetProjectLeadsFile(path)
			applied = append(applied, "HTTP_API_PROJECT_LEADS_FILE")
		}
	}

	// The access point assignment mapfile and REST token live outside the
	// configuration too, and editing the mapfile is the usual reason to
	// reconfigure a multi-AP server. The backends themselves are built
	// at startup; their files are re-read here.
	if configValue(cfg, "HTTP_API_SCHEDD_CONSTRAINT") != "" {
		w.srv.ReloadAPAssignment()
	}

	// Values can be secrets, so log which parameters changed, never what
	// they changed to.
	if len(applied) > 0 {
		w.logger.Info(logging.DestinationGeneral, "reconfigure: applied configuration changes",
			"parameters", applied)
	}
	if len(needRestart) > 0 {
		w.logger.Warn(logging.DestinationGeneral,
			"reconfigure: these parameters changed but are only read at startup; the running server still uses the old values. Restart htcondor-api to apply them",
			"parameters", needRestart)
	}
	if len(applied) == 0 && len(needRestart) == 0 {
		w.logger.Info(logging.DestinationGeneral, "reconfigure: no known parameter changed")
	}
}

// diff applies every changed dynamic parameter and returns the names of what it
// applied and what it could not, each sorted. It updates the remembered values,
// so a restart-only parameter is reported when it changes and not on every
// reconfigure thereafter.
func (w *reconfigWatcher) diff(cfg *config.Config) (applied, needRestart []string) {
	for _, p := range w.params {
		newVal := configValue(cfg, p.name)
		if newVal == w.prev[p.name] {
			continue
		}
		// Record the new value either way. A restart-only parameter is
		// reported once, when it changes, rather than on every
		// reconfigure from here until the daemon is restarted.
		w.prev[p.name] = newVal
		if p.apply == nil {
			needRestart = append(needRestart, p.name)
			continue
		}
		p.apply(w.srv, newVal)
		applied = append(applied, p.name)
	}
	sort.Strings(applied)
	sort.Strings(needRestart)
	return applied, needRestart
}

// configValue returns the resolved value of a parameter, or "" when it is
// unset. Unset and empty compare equal on purpose: for every parameter here
// they mean the same thing to the server, and distinguishing them would report
// a change when a config file merely spells the default out.
func configValue(cfg *config.Config, name string) string {
	if v, ok := cfg.Get(name); ok {
		return v
	}
	return ""
}
