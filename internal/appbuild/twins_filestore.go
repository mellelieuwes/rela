//go:build !postgres && !sqlite

package appbuild

// twinStoreGap names this build's database backend when it has no twin store,
// for buildTwins to refuse a schema declaring pacts. Empty here: the fs,
// memory and desktop tiers keep twins in filetwins.
//
// A build constant rather than a recipe override because every assembly of a
// build must agree, including a schema re-assembly through
// [SharedBase.Assemble], which carries no recipe overrides — a postgres
// reload must not quietly fall back to node-local files.
const twinStoreGap = ""
