package archive

import "time"

const CanonicalTimestampLayout = "2006-01-02T15:04:05.000000Z"

// CanonicalTimestamp normalizes externally supplied exact-evidence timestamps
// to UTC microseconds, the precision PostgreSQL timestamptz preserves.
func CanonicalTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// FormatCanonicalTimestamp returns the stable hash representation.
func FormatCanonicalTimestamp(value time.Time) string {
	return CanonicalTimestamp(value).Format(CanonicalTimestampLayout)
}
