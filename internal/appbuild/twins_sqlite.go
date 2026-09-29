//go:build sqlite

package appbuild

// twinStoreGap: the sqlite build has no twin store yet (stage 1), and twins
// in files beside rela.db would not travel with it, so pacts are refused.
// See twins_filestore.go.
const twinStoreGap = "sqlite"
