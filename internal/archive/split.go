// Package archive detects, extracts and creates archives.
package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Kind classifies split-archive naming schemes.
type Kind int

const (
	NumericSuffix     Kind = iota + 1 // name.ext.001
	PartInfix                         // name.part1.rar
	NumericSuffixOnly                 // name.001
	RarR                              // name.r00
	ZipZ                              // name.z01
)

// SplitInfo describes a file that belongs to a multi-part archive.
type SplitInfo struct {
	Kind    Kind
	Prefix  string
	Ext     string
	Part    int
	Pattern *regexp.Regexp // matches every part of the set
	First   string         // filename of part one
}

var (
	reNumExt  = regexp.MustCompile(`(?i)^(.*)\.([a-zA-Z0-9]+)\.(\d+)$`)
	rePart    = regexp.MustCompile(`(?i)^(.*?)\.part(\d+)[^.]*\.([a-zA-Z0-9]+)$`)
	reNumOnly = regexp.MustCompile(`(?i)^(.*)\.(\d+)$`)
	reR       = regexp.MustCompile(`(?i)^(.*)\.r(\d+)$`)
	reZ       = regexp.MustCompile(`(?i)^(.*)\.z(\d+)$`)
)

func padded(width int, n int) string {
	if width > 1 {
		return fmt.Sprintf("%0*d", width, n)
	}
	return strconv.Itoa(n)
}

// SplitArchiveInfo returns split-archive details or nil.
func SplitArchiveInfo(name string) *SplitInfo {
	if m := reNumExt.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[3])
		q := regexp.QuoteMeta
		return &SplitInfo{Kind: NumericSuffix, Prefix: m[1], Ext: m[2], Part: n,
			Pattern: regexp.MustCompile(`(?i)^` + q(m[1]) + `\.` + q(m[2]) + `\.\d+$`),
			First:   fmt.Sprintf("%s.%s.%s", m[1], m[2], padded(len(m[3]), 1))}
	}
	if m := rePart.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		q := regexp.QuoteMeta
		return &SplitInfo{Kind: PartInfix, Prefix: m[1], Ext: m[3], Part: n,
			Pattern: regexp.MustCompile(`(?i)^` + q(m[1]) + `\.part\d+.*\.` + q(m[3]) + `$`),
			First:   fmt.Sprintf("%s.part%s.%s", m[1], padded(len(m[2]), 1), m[3])}
	}
	if m := reNumOnly.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &SplitInfo{Kind: NumericSuffixOnly, Prefix: m[1], Part: n,
			Pattern: regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(m[1]) + `\.\d+$`),
			First:   fmt.Sprintf("%s.%s", m[1], padded(len(m[2]), 1))}
	}
	if m := reR.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &SplitInfo{Kind: RarR, Prefix: m[1], Ext: "rar", Part: n + 1,
			Pattern: regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(m[1]) + `\.r\d+$`), First: m[1] + ".rar"}
	}
	if m := reZ.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &SplitInfo{Kind: ZipZ, Prefix: m[1], Ext: "zip", Part: n + 1,
			Pattern: regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(m[1]) + `\.z\d+$`), First: m[1] + ".zip"}
	}
	return nil
}

// IsFirstSplitPartOfArchive reports whether name is part one of a split set
// whose base extension is an archive extension (or absent).
func IsFirstSplitPartOfArchive(name string) bool {
	si := SplitArchiveInfo(name)
	if si == nil || si.Part != 1 {
		return false
	}
	return si.Ext == "" || archiveExts["."+strings.ToLower(si.Ext)]
}

// NormalizeSplitNames renames parts with noisy suffixes (part1-hash.rar) to a
// clean scheme so extractors find every volume. It returns old->new paths.
func NormalizeSplitNames(dir string) map[string]string {
	renamed := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return renamed
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		si := SplitArchiveInfo(e.Name())
		if si == nil {
			continue
		}
		var clean string
		switch si.Kind {
		case PartInfix:
			clean = fmt.Sprintf("%s.part%d.%s", si.Prefix, si.Part, si.Ext)
		case NumericSuffix:
			clean = fmt.Sprintf("%s.%s.%03d", si.Prefix, si.Ext, si.Part)
		case NumericSuffixOnly:
			clean = fmt.Sprintf("%s.%03d", si.Prefix, si.Part)
		case RarR:
			clean = fmt.Sprintf("%s.r%02d", si.Prefix, si.Part-1)
		case ZipZ:
			clean = fmt.Sprintf("%s.z%02d", si.Prefix, si.Part-1)
		}
		if clean == "" || clean == e.Name() {
			continue
		}
		src, dst := filepath.Join(dir, e.Name()), filepath.Join(dir, clean)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.Rename(src, dst); err == nil {
			renamed[src] = dst
		}
	}
	return renamed
}
