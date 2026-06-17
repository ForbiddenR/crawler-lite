package version

// Set via -ldflags at build time, e.g.:
//
//   go build -ldflags "-X github.com/yourteam/crawler-lite/internal/version.Version=$(git rev-parse --short HEAD)"
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
