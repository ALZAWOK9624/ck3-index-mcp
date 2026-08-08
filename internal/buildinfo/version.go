package buildinfo

// Version is injected from the repository VERSION file at build time.
// Local `go run` and unversioned test binaries deliberately report dev.
var Version = "dev"

// Revision is the source commit the binary was built from, injected the same
// way. Version alone moves once per release, so a locally built server can sit
// many commits behind its own source and still report a plausible version —
// which is exactly how a rebuilt tool goes missing without anyone noticing.
// ck3_health publishes this so the gap is visible rather than inferred.
var Revision = "unknown"
