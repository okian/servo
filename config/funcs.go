package config

import (
	"strings"
	"time"
)

func SetDefault(k string, v interface{}) { config.SetDefault(k, v) }
func GetInt(k string) int                { return config.GetInt(k) }
func GetInt32(k string) int32            { return config.GetInt32(k) }
func GetInt64(k string) int64            { return config.GetInt64(k) }
func GetString(k string) string          { return config.GetString(k) }
func GetBool(k string) bool              { return config.GetBool(k) }
func GetFloat64(k string) float64        { return config.GetFloat64(k) }
func GetTime(k string) time.Time         { return config.GetTime(k) }
func GetDuration(k string) time.Duration { return config.GetDuration(k) }
func GetUint(k string) uint              { return config.GetUint(k) }
func GetUint32(k string) uint32          { return config.GetUint32(k) }
func GetUint64(k string) uint64          { return config.GetUint64(k) }
func GetStringSlice(k string) []string   { return strings.Split(config.GetString(k), ",") }
