package config

import (
	"sort"
	"strings"
)

// Some settings come in families: one group of parameters per named
// thing, with the name in the middle of the parameter.
//
//	HTTP_API_PICKER_<instance>_API_KEY
//	HTTP_API_CONNECTOR_<instance>_GROUPS
//	HTTP_API_SITE_APP_<instance>_ENABLED
//
// The name has to be folded into something a parameter name can hold,
// and that fold is not injective unless the name is constrained: two
// instances differing only by hyphen-versus-underscore land on one set
// of parameters. Each family used to carry its own copy of the fold,
// and only some of them checked for the collision it creates.

// InstanceID folds an instance name into the form a parameter name
// uses: upper case, with hyphens turned into underscores.
func InstanceID(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// InstanceCollisions reports names that fold onto one parameter name,
// keyed by that shared name, each group sorted. An empty result means
// every name has parameters of its own.
//
// A family whose names cannot contain an underscore has an injective
// fold and will never see a collision; calling this is then a guard
// rather than a necessity. A family that does not constrain its names
// needs it, because the alternative is two instances silently sharing
// one configuration.
func InstanceCollisions(names []string) map[string][]string {
	byID := map[string][]string{}
	for _, n := range names {
		id := InstanceID(n)
		byID[id] = append(byID[id], n)
	}
	out := map[string][]string{}
	for id, group := range byID {
		if len(group) > 1 {
			sort.Strings(group)
			out[id] = group
		}
	}
	return out
}
