// Package connectors holds the credentials this server obtains on a
// user's behalf: what a connector is, where its definition comes from,
// and what it is called in each of the three places that name it.
//
// See design_notes/CONNECTORS.md. The organizing fact is that HTCondor's
// credential-bootstrap mechanisms differ by who holds the OAuth2 client
// registration, so what generalizes here is the identity and the entry
// point, not the storage.
package connectors

import (
	"fmt"
	"sort"
	"strings"
)

// Namespace says who defines a connector.
//
// It is a closed set, and that is the point of it. `builtin` is ours:
// the definitions ship with the server and an operator supplies only the
// client registration. `site` is the operator's, and nothing we ever add
// can collide with a name in it.
//
// A third namespace -- a fetched catalogue, a vendor's definitions --
// slots in here without renaming anything that already exists, which is
// what a flat space with a few magic names could not do.
type Namespace string

const (
	// Builtin definitions ship with this server and are not extensible
	// by configuration. See builtin.go.
	Builtin Namespace = "builtin"
	// Site definitions are entirely the operator's.
	Site Namespace = "site"
)

// Namespaces returns every namespace, in a fixed order.
func Namespaces() []Namespace { return []Namespace{Builtin, Site} }

// maxNameLen bounds the name half of an id. It becomes a URL path
// segment, a configuration parameter name and part of a credd file
// name; nothing legible needs more.
const maxNameLen = 64

// ID names one connector: a namespace and a name within it.
//
// The zero value is not a valid id. Build one with ParseID or New.
type ID struct {
	Namespace Namespace
	Name      string
}

// New builds an id and checks it.
func New(ns Namespace, name string) (ID, error) {
	id := ID{Namespace: ns, Name: name}
	return id, id.Validate()
}

// MustParse is ParseID for ids written as constants in this package's
// own code. It panics, so it is never reachable from a request.
func MustParse(s string) ID {
	id, err := ParseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// String is the id as a person and an API see it: "builtin:gdrive".
//
// NOTE that this is also a syntactically valid URL with the scheme
// "builtin" -- url.Parse("builtin:gdrive") returns scheme "builtin",
// opaque "gdrive", empty path. An id is therefore only ever a path
// SEGMENT, under /api/v1/connect/, and never a relative reference on its
// own. See TestIDIsNotAUsableRelativeReference.
func (id ID) String() string {
	return string(id.Namespace) + ":" + id.Name
}

// Validate reports whether an id is one this server will act on.
//
// An unknown namespace is an error rather than a passthrough: the
// closedness of `builtin` is what lets us change a shipped definition in
// a release, and a namespace nobody declared would quietly become a
// third kind of connector with no rules attached.
func (id ID) Validate() error {
	switch id.Namespace {
	case Builtin, Site:
	case "":
		return fmt.Errorf("connector id has no namespace; expected one of %s", namespaceList())
	default:
		return fmt.Errorf("connector namespace %q is not one of %s", id.Namespace, namespaceList())
	}
	return validateName(id.Name)
}

func namespaceList() string {
	out := make([]string, 0, len(Namespaces()))
	for _, ns := range Namespaces() {
		out = append(out, string(ns)+":")
	}
	return strings.Join(out, ", ")
}

// validateName checks the name half.
//
// Lowercase letters, digits and hyphen. No underscore, and that
// exclusion does two jobs.
//
// It is what lets the credd service name appear in a URL SCHEME. A job
// addresses a connector's file as <service>+gdrive2://..., and
// HTCondor's IsUrl accepts only alphanumerics, '+', '-' and '.' in a
// scheme (condor_url.cpp) -- an underscore anywhere in it means the
// string is not recognised as a URL at all, so the schedd would treat
// it as a plain filename and the job would fail looking for it.
//
// And it makes MacroID injective. That mapping turns '-' into '_' for
// configuration parameter names, so with underscores allowed in names
// site:my-app and site:my_app would be two connectors and one
// parameter. Banning one of the two spellings removes the collision
// rather than detecting it.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("connector name is empty")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("connector name %q is longer than %d characters", name, maxNameLen)
	}
	if !isNameAlnum(rune(name[0])) || !isNameAlnum(rune(name[len(name)-1])) {
		return fmt.Errorf("connector name %q must begin and end with a lowercase letter or digit", name)
	}
	for _, r := range name {
		if isNameAlnum(r) || r == '-' {
			continue
		}
		return fmt.Errorf("connector name %q contains %q; names may use only "+
			"lowercase letters, digits and hyphen", name, r)
	}
	return nil
}

func isNameAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// ParseID reads "namespace:name".
//
// Split on the FIRST colon. A second one is a bad character in the name
// and is reported as one, rather than being swallowed into a name that
// then fails somewhere less informative.
func ParseID(s string) (ID, error) {
	ns, name, found := strings.Cut(s, ":")
	if !found {
		return ID{}, fmt.Errorf("connector id %q has no namespace; expected %s<name>",
			s, namespaceList())
	}
	id := ID{Namespace: Namespace(ns), Name: name}
	if err := id.Validate(); err != nil {
		return ID{}, err
	}
	return id, nil
}

// configPrefix is the start of every connector parameter name.
const configPrefix = "HTTP_API_CONNECTOR_"

// MacroID is the id as it appears inside a configuration parameter name:
// upper-cased, namespace and name joined by an underscore, hyphens
// turned into underscores.
//
// Both substitutions are forced on us, as they are for site apps.
// HTCondor parameter names are case-insensitive, and a hyphen inside one
// is legal to set but reads as subtraction wherever a name appears in an
// expression.
//
// Injective, because a connector name may not contain an underscore
// (validateName): nothing else maps onto a name a hyphen produces.
// MacroCollision remains as a guard rather than a necessity.
func (id ID) MacroID() string {
	return strings.ToUpper(strings.ReplaceAll(
		string(id.Namespace)+"_"+id.Name, "-", "_"))
}

// ConfigKey is the full parameter name for one of a connector's
// settings, e.g. ConfigKey("GROUPS") on builtin:gdrive gives
// HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS.
func (id ID) ConfigKey(setting string) string {
	return configPrefix + id.MacroID() + "_" + setting
}

// MacroCollision reports ids that share a configuration parameter name,
// keyed by that shared name. An empty result means every id maps to a
// name of its own.
//
// Two connectors whose names differ only by hyphen-versus-underscore are
// a configuration the operator cannot express: there is one
// HTTP_API_CONNECTOR_SITE_MY_APP_GROUPS and two connectors wanting it.
// Refusing both is the fail-closed answer, the same choice site apps
// makes -- enabling either would be enabling it under a configuration
// that might have been written for the other, and these configurations
// carry client registrations.
func MacroCollision(ids []ID) map[string][]ID {
	byMacro := map[string][]ID{}
	for _, id := range ids {
		byMacro[id.MacroID()] = append(byMacro[id.MacroID()], id)
	}
	out := map[string][]ID{}
	for m, group := range byMacro {
		if len(group) > 1 {
			sort.Slice(group, func(i, j int) bool { return group[i].String() < group[j].String() })
			out[m] = group
		}
	}
	return out
}

// DefaultServicePrefix is HTTP_API_CONNECTOR_PREFIX's default.
const DefaultServicePrefix = "webapi"

// Service is the name this connector's credential is stored under in the
// credd, and the credential half of a job's file reference:
// "<prefix>-<namespace>-<name>".
//
// Hyphens, not underscores, because this string becomes part of a URL
// SCHEME -- <service>+gdrive2://<id>/<name> -- and HTCondor's IsUrl
// accepts no underscore in one. A service name with one would not be
// recognised as a URL at all.
//
// The credd's cred dir is shared with the administrator's own credmons
// and nothing arbitrates, so a credential stored as plain "gdrive" is
// the same file as theirs -- we would clobber their token, or their
// credmon would refresh ours using their client registration. The prefix
// is the namespace that prevents it, and it is configurable because two
// API servers in front of one access point would otherwise collide with
// each other having both avoided the administrator.
//
// The connector's OWN namespace is in here too, so that builtin:gdrive
// and site:gdrive -- different client registrations, different
// applications -- cannot land on one file. That costs a longer name in
// use_oauth_services and buys an injective mapping, which is the better
// trade: the alternative is a collision check that fails closed and
// refuses a configuration an operator is entitled to write.
func (id ID) Service(prefix string) string {
	if prefix == "" {
		prefix = DefaultServicePrefix
	}
	return prefix + "-" + string(id.Namespace) + "-" + id.Name
}
