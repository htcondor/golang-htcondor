package htcondor

import (
	"fmt"
	"strings"

	"github.com/PelicanPlatform/classad/classad"
)

// setOutputDirectory is the organized output half of
// SubmitHash::SetOutputDirSpecial, which HTCONDOR-3618 added.
//
// The organized output commands build an output directory per job out of a
// base directory and a pattern, so a cluster's output lands in a tree instead
// of one flat directory. They write OutputDirectory, the same attribute
// output_directory writes, which is why upstream moved the pair out of the
// table-driven pass into a setter of its own.
//
// Here output_directory stays an ordinary table row. It has always been
// f_genfile, so the keyword pass already resolves it against Iwd exactly as
// SetOutputDirSpecial does, and keeping the row is what lets this parser go on
// matching an HTCondor from before the split. This setter therefore adds only
// what the table cannot express: the organized output commands, and the rule
// that output_directory beats them.
//
// When the submit file gives both, output_directory wins. HTCondor warns on
// that; this library has no warning channel, and the ad is the same either
// way.
func (sf *SubmitFile) setOutputDirectory(ad *classad.ClassAd) error {
	// The organized output settings are read even when output_directory
	// wins, because reading them is what reports a pattern that is
	// configured but unusable. Only the containment check is skipped --
	// SetOutputDirSpecial runs it inside the branch that uses the result.
	organized, pattern, err := sf.organizedOutputDirectory()
	if err != nil {
		return err
	}
	if organized == "" {
		return nil
	}
	// output_directory wins, and the keyword pass has already written it.
	if _, ok := sf.simpleKeywordParam("output_directory", "OutputDirectory"); ok {
		return nil
	}
	if pattern != "" && !isContainedRelativePath(pattern) {
		return fmt.Errorf("cannot do organized_output because the pattern %s is not a subdirectory of the organized_output_directory or the job's initial working directory", pattern)
	}
	iwd, err := sf.jobIwd()
	if err != nil {
		return err
	}
	_ = ad.Set("OutputDirectory", fullPath(organized, iwd))
	return nil
}

// organizedOutputDirectory is SubmitHash::UnexpandedOrganizedOutputDirectory:
// the directory the organized output commands ask for, and the pattern half of
// it on its own so the caller can check that it stays put.
//
// Organized output is on when organized_output says so, and also when it says
// nothing but a base directory or a pattern was given -- naming either is
// taken as asking for it. An explicit organized_output = false turns it off
// however the other two are set.
//
// The pattern falls back to ORGANIZED_OUTPUT_PATTERN, whose default spells one
// directory per cluster and one per job inside it. A pattern that is missing
// altogether leaves nothing to build a per-job directory from, and HTCondor
// treats that as an error rather than quietly writing the base directory.
//
// Returned empty when organized output is off, which is the common case.
func (sf *SubmitFile) organizedOutputDirectory() (dir, pattern string, err error) {
	base, _ := sf.submitCommand("organized_output_directory")
	pattern, _ = sf.submitCommand("organized_output_pattern")

	on, explicit := false, false
	if raw, ok := sf.submitCommand("organized_output"); ok {
		b, valid := evalBoolParam(raw)
		if !valid {
			return "", "", fmt.Errorf("organized_output=%s is invalid, must eval to a boolean", raw)
		}
		on, explicit = b, true
	}
	if !explicit && (base != "" || pattern != "") {
		on = true
	}
	if !on {
		return "", "", nil
	}

	if pattern == "" {
		pattern, _ = sf.cfg.Get("ORGANIZED_OUTPUT_PATTERN")
	}
	if pattern == "" {
		return "", "", fmt.Errorf("cannot do organized_output because organized_output_pattern is not defined")
	}

	if base == "" {
		return pattern, pattern, nil
	}
	// dircat: the base loses its trailing separators, the pattern its
	// leading ones, and one separator joins them.
	return strings.TrimRight(base, "/\\") + "/" + strings.TrimLeft(pattern, "/\\"), pattern, nil
}

// isContainedRelativePath is submit_utils.cpp's is_contained_relative_path: a
// relative path none of whose components is "..", so that joining it to a base
// directory cannot land outside that base.
//
// A path that is already absolute is not contained: it ignores the base
// entirely rather than escaping it, and either way the base stops meaning
// anything. The check is textual -- it does not resolve symlinks, so a
// component that is a link out of the tree still passes, exactly as upstream.
func isContainedRelativePath(path string) bool {
	if strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
