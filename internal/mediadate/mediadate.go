// Package mediadate holds small date-parsing helpers shared by the
// metadata clients (tmdb, tvdb, seerr), which all need to pull a release
// year out of a "YYYY-MM-DD"-shaped date string returned by their
// respective APIs.
package mediadate

import "strconv"

// Year extracts the leading 4-digit year from a date string such as
// "2021-03-15". Returns 0 if value is too short or doesn't start with a
// valid year.
func Year(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil {
		return 0
	}
	return year
}
