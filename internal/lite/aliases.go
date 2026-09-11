package lite

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"mindmory.local/core/internal/retrieval"
)

// LoadAliases installs the optional overlay before serving requests. Invalid
// overlays leave the built-in table intact. Logs never include file contents.
func (s *Server) LoadAliases(path string) {
	entries, err := readAliasOverlay(path)
	if err != nil {
		s.Log.Warn("alias overlay skipped; using built-in aliases", "reason", err.Error())
		s.Aliases = retrieval.NewAliasExpander(nil)
		return
	}
	s.Aliases = retrieval.NewAliasExpander(entries)
}

func readAliasOverlay(path string) ([]retrieval.AliasEntry, error) {
	defaults := retrieval.DefaultAliases()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return nil, errors.New("cannot open alias file")
	}
	defer f.Close()
	const maxBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, errors.New("alias file unreadable or exceeds 1 MiB")
	}
	var overlay []retrieval.AliasEntry
	if json.Unmarshal(data, &overlay) != nil || strings.TrimSpace(string(data)) == "null" {
		return nil, errors.New("alias file must be a JSON array")
	}
	if len(overlay) > 1000 {
		return nil, errors.New("alias file exceeds 1000 entries")
	}
	// Same-canonical entries append aliases in file order. Built-ins are retained.
	merged := defaults
	positions := map[string]int{}
	for i, entry := range merged {
		positions[strings.ToLower(entry.Canonical)] = i
	}
	for _, entry := range overlay {
		entry.Canonical = strings.TrimSpace(entry.Canonical)
		if entry.Canonical == "" || len(entry.Canonical) > 1024 || len(entry.Aliases) == 0 || len(entry.Aliases) > 100 {
			return nil, errors.New("invalid alias entry")
		}
		for i, alias := range entry.Aliases {
			entry.Aliases[i] = strings.TrimSpace(alias)
			if entry.Aliases[i] == "" || len(alias) > 4096 {
				return nil, errors.New("invalid alias phrase")
			}
		}
		key := strings.ToLower(entry.Canonical)
		index, exists := positions[key]
		if !exists {
			index = len(merged)
			positions[key] = index
			merged = append(merged, retrieval.AliasEntry{Canonical: entry.Canonical})
		}
		// Copy before append so defaults cannot be changed through shared backing arrays.
		aliases := append([]string(nil), merged[index].Aliases...)
		seen := map[string]bool{}
		for _, alias := range aliases {
			seen[strings.ToLower(alias)] = true
		}
		for _, alias := range entry.Aliases {
			if !seen[strings.ToLower(alias)] {
				aliases = append(aliases, alias)
				seen[strings.ToLower(alias)] = true
			}
		}
		merged[index].Aliases = aliases
	}
	return merged, nil
}
