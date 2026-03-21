package version

// Version is populated via -ldflags during build time.
var Version = "dev"

// Info returns the current application version.
func Info() string {
	return Version
}
