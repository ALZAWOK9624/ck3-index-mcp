package buildinfo

// Version is injected from the repository VERSION file at build time.
// Local `go run` and unversioned test binaries deliberately report dev.
var Version = "dev"
