//go:build postgres

package appbuild

// twinStoreGap: the postgres build has no twin store yet (stage 1), and
// filetwins is node-local, so pacts are refused. See twins_filestore.go.
const twinStoreGap = "postgres"
