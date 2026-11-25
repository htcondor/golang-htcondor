// Package knobs is the HTCondor API server's configuration settings,
// generated from knobs.yaml.
//
// A setting the server reads has to be declared there, because
// config.Get accepts only a declared knob: there is no way to spell a
// name the table does not carry. That is what stops a setting existing
// in the code and being missing from the reference, and a knob
// documented in the reference that nothing reads.
//
// Use the generated identifiers:
//
//	timeout := knobs.ReadTimeout.Get(cfg)
//
//go:generate go run ./generate.go -docs ../../docs/operator/config.md
package knobs
