// Package common provides shared utilities for all translator packages.
package common

// StringsToBytes converts a []string to [][]byte without extra allocations per element.
func StringsToBytes(ss []string) [][]byte {
	if ss == nil {
		return nil
	}
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}
