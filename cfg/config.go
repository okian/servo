package cfg

import "time"

func GetString(k string) string {
	return vp.GetString(k)
}

func GetInt(k string) int {
	return vp.GetInt(k)
}

func GetInt32(k string) int32 {
	return vp.GetInt32(k)
}

func GetInt64(k string) int64 {
	return vp.GetInt64(k)
}

func GetBool(k string) bool {
	return vp.GetBool(k)
}

func GetUint(k string) uint {
	return vp.GetUint(k)
}

func GetUint32(k string) uint32 {
	return vp.GetUint32(k)
}

func GetUint64(k string) uint64 {
	return vp.GetUint64(k)
}

func GetFloat64(k string) float64 {
	return vp.GetFloat64(k)
}

func GetGet(k string) interface{} {
	return vp.Get(k)
}

func GetDuration(k string) time.Duration {
	return vp.GetDuration(k)
}

func GetTime(k string) time.Time {
	return vp.GetTime(k)
}

func SetDefault(k string, v interface{}) {
	vp.SetDefault(k, v)
}

var (
	// The values must be set on build time
	app     string
	version string
	commit  string
	tag     string
	branch  string
	// date of build
	date string
)

func AppName() string {
	return app
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

func Date() string {
	return date
}
