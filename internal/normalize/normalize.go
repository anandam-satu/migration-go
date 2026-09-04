// Package normalize ports util/NormalizationUtil.java (used by the stock
// migration to compute normalized_item_name).
package normalize

import (
	"regexp"
	"strings"
)

var (
	reNonAlnum = regexp.MustCompile(`[^A-Z0-9 ]`)
	reSpaces   = regexp.MustCompile(`\s+`)
	reCompact  = regexp.MustCompile(`[^A-Z0-9]`)
)

// NormalizeItemName uppercases and collapses any character outside A-Z0-9 and
// space into a single space, then trims. Example:
//
//	"NB ACER AG14-72P-58AK" -> "NB ACER AG14 72P 58AK"
func NormalizeItemName(name string) string {
	if name == "" {
		return ""
	}
	s := strings.ToUpper(name)
	s = strings.ReplaceAll(s, "™", "")
	s = strings.ReplaceAll(s, "®", "")
	s = reNonAlnum.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// NormalizeCompact uppercases and removes every character outside A-Z0-9.
func NormalizeCompact(value string) string {
	if value == "" {
		return ""
	}
	return reCompact.ReplaceAllString(strings.ToUpper(value), "")
}
