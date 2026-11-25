// Code generated from knobs.yaml by go generate; DO NOT EDIT.

package knobs

import (
	"time"

	"github.com/htcondor/golang-htcondor/config"
)

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

// EnableIDP is HTTP_API_ENABLE_IDP.
//
// Run the built-in identity provider, so this server signs users in
// itself instead of sending them to an external one. Off by default.
// The provider shares the unified application database; the separate
// HTTP_API_IDP_DB_PATH it once had is no longer read.
var EnableIDP = config.DefineBool("HTTP_API_ENABLE_IDP", false, config.Startup,
	"Run the built-in identity provider, so this server signs users in itself instead of sending them to an external one. Off by default. The provider shares the unified application database; the separate HTTP_API_IDP_DB_PATH it once had is no longer read.")

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

// MCPAdminGroup is HTTP_API_MCP_ADMIN_GROUP.
//
// Obsolete: do not use it in new configuration. Group(s) whose members
// may read every user's jobs through MCP. Read as a raw string and
// split downstream, so a change to how lists are parsed does not
// change who is privileged.
//
// Deprecated: do not use it in new configuration.
var MCPAdminGroup = config.DefineString("HTTP_API_MCP_ADMIN_GROUP", "", config.Reconfig,
	"Obsolete: do not use it in new configuration. Group(s) whose members may read every user's jobs through MCP. Read as a raw string and split downstream, so a change to how lists are parsed does not change who is privileged.")

// MCPSuperuserGroup is HTTP_API_MCP_SUPERUSER_GROUP.
//
// Obsolete: do not use it in new configuration. Group(s) whose members
// may change any user's jobs through MCP. Read as a raw string and
// split downstream, as above.
//
// Deprecated: do not use it in new configuration.
var MCPSuperuserGroup = config.DefineString("HTTP_API_MCP_SUPERUSER_GROUP", "", config.Reconfig,
	"Obsolete: do not use it in new configuration. Group(s) whose members may change any user's jobs through MCP. Read as a raw string and split downstream, as above.")

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

// ReadTimeout is HTTP_API_READ_TIMEOUT.
//
// How long a request may take to arrive in full, as a Go duration. A
// value that cannot be parsed is logged and the default applies.
var ReadTimeout = config.DefineDuration("HTTP_API_READ_TIMEOUT", time.Duration(30000000000) /* 30s */, config.Startup,
	"How long a request may take to arrive in full, as a Go duration. A value that cannot be parsed is logged and the default applies.")

// SpoolBufferDir is HTTP_API_SPOOL_BUFFER_DIR.
//
// Where an archive dropped in the SSH gateway's /submit/, or posted to
// the REST upload endpoints, is buffered while it is unpacked. Unset
// holds it in memory instead, under a much smaller ceiling.
var SpoolBufferDir = config.DefineString("HTTP_API_SPOOL_BUFFER_DIR", "", config.Startup,
	"Where an archive dropped in the SSH gateway's /submit/, or posted to the REST upload endpoints, is buffered while it is unpacked. Unset holds it in memory instead, under a much smaller ceiling.")

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
// TLS.
var TLSCert = config.DefineString("HTTP_API_TLS_CERT", "", config.Startup,
	"PEM certificate for serving HTTPS directly, rather than behind a terminating proxy. Needs HTTP_API_TLS_KEY; neither alone enables TLS.")

// TLSKey is HTTP_API_TLS_KEY.
//
// Private key for HTTP_API_TLS_CERT. Keep it readable only by the
// account the server runs as.
var TLSKey = config.DefineString("HTTP_API_TLS_KEY", "", config.Startup,
	"Private key for HTTP_API_TLS_CERT. Keep it readable only by the account the server runs as.")

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
// Accept the user header from any source address. For demonstration
// and testing only: with it on, anything that can reach this server
// can claim to be any user, so it undoes authentication rather than
// relaxing it. Off by default, and the server logs a warning for as
// long as it is on. Leave it off anywhere a real person has an
// account.
var UserHeaderTrustAny = config.DefineBool("HTTP_API_USER_HEADER_TRUST_ANY", false, config.Startup,
	"Accept the user header from any source address. For demonstration and testing only: with it on, anything that can reach this server can claim to be any user, so it undoes authentication rather than relaxing it. Off by default, and the server logs a warning for as long as it is on. Leave it off anywhere a real person has an account.")

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
var ConnectorInstances = config.DefineList("HTTP_API_CONNECTORS", nil, config.Reconfig,
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
// File holding the OAuth2 client secret. A connector with no readable
// secret is refused rather than offered. Holds or names a credential:
// keep it readable only by the account the server runs as.
func (s ConnectorSettings) ClientSecretFile() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_CLIENT_SECRET_FILE", "", config.Reconfig,
		"File holding the OAuth2 client secret. A connector with no readable secret is refused rather than offered. Holds or names a credential: keep it readable only by the account the server runs as.")
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
// The scopes requested when a user connects this account.
func (s ConnectorSettings) Scopes() config.ListKnob {
	return config.DefineList("HTTP_API_CONNECTOR_"+s.id+"_SCOPES", nil, config.Reconfig,
		"The scopes requested when a user connects this account.")
}

// Groups is HTTP_API_CONNECTOR_<instance>_GROUPS.
//
// Group(s) whose members may connect this account. Unset offers it to
// everyone who may sign in.
func (s ConnectorSettings) Groups() config.ListKnob {
	return config.DefineList("HTTP_API_CONNECTOR_"+s.id+"_GROUPS", nil, config.Reconfig,
		"Group(s) whose members may connect this account. Unset offers it to everyone who may sign in.")
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
var PickerInstances = config.DefineList("HTTP_API_PICKERS", nil, config.Reconfig,
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
func (s SiteAppSettings) Groups() config.ListKnob {
	return config.DefineList("HTTP_API_SITE_APP_"+s.id+"_GROUPS", nil, config.Reconfig,
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
	BuildDefaultCPUs.Knob,
	BuildDefaultDiskMB.Knob,
	BuildDefaultMemoryMB.Knob,
	BuildExtraSubmit.Knob,
	BuildMaxCPUs.Knob,
	BuildMaxDiskMB.Knob,
	BuildMaxMemoryMB.Knob,
	BuildRequirements.Knob,
	BuildStagingBase.Knob,
	EnableIDP.Knob,
	IdleTimeout.Knob,
	IDPAccessTokenLifespan.Knob,
	IDPIssuer.Knob,
	IDPRefreshTokenLifespan.Knob,
	Listen.Knob,
	MCPAdminGroup.Knob,
	MCPSuperuserGroup.Knob,
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
	ReadTimeout.Knob,
	SpoolBufferDir.Knob,
	TLSCACert.Knob,
	TLSCert.Knob,
	TLSKey.Knob,
	TokenRetention.Knob,
	UserHeaderTrustAny.Knob,
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
	"HTTP_API_MCP_ADMIN_GROUP":     "",
	"HTTP_API_MCP_SUPERUSER_GROUP": "",
}
