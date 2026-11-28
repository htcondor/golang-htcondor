package fuzzsubmit

import "github.com/htcondor/golang-htcondor/fuzz/submit/oracle"

// nonTableKeywords are submit commands SubmitHash reads directly, with no row
// in prunable_keywords.
//
// get_submit_keywords() returns that table and nothing else, so NewVocab --
// which learns every keyword from it -- cannot know these exist. Neither can
// TestSimpleKeywordTableMatchesOracle or TestFileRoleKeywordsAreAccountedFor,
// which walk the same list. A command that is read by submit_param() but never
// written into the table is therefore invisible to every check here at once:
// the generator will not emit it, so no sweep compares it, and no table test
// notices it is missing from the Go side.
//
// That is not hypothetical. HTCondor reads four ARC commands in one block of
// SetGridParams; arc_rte has a table row and the other three do not, and this
// library had implemented arc_rte and arc_resources and not the remaining two.
// Nothing could have found that, because nothing could write the keyword.
//
// So they are seeded by hand. The flags are transcribed from how SubmitHash
// reads each one -- submit_param_bool is f_as_bool, a plain submit_param of a
// string is f_as_string, and the grid group puts a keyword in grid files,
// where SetGridParams is the only code that looks at it.
//
// TestNonTableKeywordsAreStillAbsent holds the list honest: an entry that
// upstream later gives a real table row is a duplicate, and fails there rather
// than quietly being emitted twice.
//
//nolint:unused // read by NewVocab and the tagged tests
var nonTableKeywords = []oracle.Keyword{
	// SetGridParams, read beside arc_rte, which does have a row.
	{Key: "arc_application", Attr: "ArcApplication", Opts: oracle.KeyAsString | specialGrid},
	{Key: "arc_data_staging", Attr: "ArcDataStaging", Opts: oracle.KeyAsString | specialGrid},
	{Key: "arc_resources", Attr: "ArcResources", Opts: oracle.KeyAsString | specialGrid},

	// Read by their own setters, each writing an attribute of its own.
	{Key: "disk_usage", Attr: "DiskUsage", Opts: oracle.KeyAsString},
	{Key: "require_gpus", Attr: "RequireGPUs", Opts: 0},

	// Read for their effect rather than for one attribute: hold sets
	// JobStatus, want_name_tag suppresses a tag on the grid resource
	// name, use_oauth_services drives the credential attributes, and
	// retry_request_disk steers the on-exit policy. Attr is left empty,
	// which makes the generator dedupe them by keyword instead.
	{Key: "hold", Opts: oracle.KeyAsBool},
	{Key: "want_name_tag", Opts: oracle.KeyAsBool},
	{Key: "retry_request_disk", Opts: oracle.KeyAsString},
	{Key: "use_oauth_services", Opts: oracle.KeyAsString},

	// The pattern half of organized output. The other two commands do have
	// rows; this one is read with a bare lookup() so it never reached the
	// table, which is how it went unexercised when organized output was
	// implemented.
	{Key: "organized_output_pattern", Opts: oracle.KeyAsString},

	// shell replaces the executable and the arguments both: SetExecutable
	// writes /bin/sh and clears the transfer flag, SetArguments builds
	// -c <value>. It is seeded last because it only became safe to
	// generate once the Go side did the same -- before that it would have
	// needed a divergence entry on Cmd and Arguments, which would have
	// excused every other difference in them across the sweep.
	{Key: "shell", Opts: oracle.KeyAsString},
}

// nonTableSkipped are the remaining non-table commands, and why each is left
// out rather than seeded. Keeping them written down is the point: the reason
// a command is not generated should be a sentence someone can disagree with,
// not its absence from a list.
// only under the libcondor_utils tag
//
//nolint:unused // read by TestNonTableKeywordsAreStillAbsent, which builds
var nonTableSkipped = map[string]string{
	// condor_submit sets these for itself when it runs -i, and
	// SubmitHash reads them only when IsInteractiveJob, which is never
	// true for a library parse. Both sides ignore them, so emitting them
	// would cost a keyword slot per file and compare nothing.
	"interactive_executable": "read only when IsInteractiveJob, which a library parse never is",
	"interactive_args":       "read only when IsInteractiveJob, which a library parse never is",

	// Structural: the generator writes universe and queue itself, and
	// these decide what the rest of the file means rather than adding an
	// attribute to it.
	"universe":        "the generator chooses the universe and writes it itself",
	"requirements":    "the generator builds Requirements through the keywords that feed it",
	"initialdir":      "changes what every relative path in the file resolves against",
	"initial_dir":     "as initialdir",
	"job_iwd":         "as initialdir",
	"max_materialize": "late materialization, which TestFactoryAttributesReachTheClusterAd covers directly",

	// delegate_job_gsi_credentials_lifetime already has a value generator
	// under its squashed attribute spelling and reaches the sweep that
	// way.
	"delegate_job_gsi_credentials_lifetime": "reached through its attribute spelling, which byName already generates",
}
