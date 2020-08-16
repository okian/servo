package config

var (
	// The values must be set on build time
	appName string
	version string
	commit  string
	tag     string
	branch  string
)

func AppName() string {
	return appName
}

func Version() string {
	return version
}

func Commit() string {
	return commit
}

func Tag() string {
	return tag
}

func Branch() string {
	return branch
}
