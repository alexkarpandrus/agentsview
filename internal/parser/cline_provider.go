package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// newClineProviderFactory creates a provider factory for Cline CLI.
// Cline stores sessions as directories under <root>/data/sessions/<sessionId>/
// with <sessionId>.json (metadata) and <sessionId>.messages.json (transcript).
// Roots may point directly to ~/.cline or to ~/.cline/data/sessions.
func newClineProviderFactory(def AgentDef) ProviderFactory {
	return NewSingleFileProviderFactory(
		def,
		clineProviderCapabilities(),
		func(cfg ProviderConfig) singleFileSourceSet {
			return NewSingleFileSourceSet(
				def.Type,
				cfg.Roots,
				WithStreamingFileDiscovery(clineDiscoverEach),
				WithFileWatchRoots(func(roots []string) []WatchRoot {
					return clineWatchRoots(roots)
				}),
				WithFileChangedPathClassifier(
					func(root, path string, allowMissing bool) (singleFileMatch, bool) {
						return clineClassifyPath(root, path, allowMissing)
					},
				),
				WithFileLookup(func(root, rawID string) (singleFileMatch, bool) {
					return clineFindFile(root, rawID)
				}),
				WithFileFingerprint(func(src singleFileSource) (SourceFingerprint, error) {
					if _, ok := clineClassifyPath(src.Root, src.Path, false); !ok {
						return SourceFingerprint{}, fmt.Errorf(
							"cline source is outside the configured session root: %s",
							src.Path,
						)
					}
					return clineFingerprintSource(src.Path)
				}),
				WithFileStoredSourceHintScope(clineStoredSourceHintScope),
				WithFileParse(func(src singleFileSource, req ParseRequest) ([]ParseResult, []string, error) {
					return clineParseFile(src, req)
				}),
			)
		},
	)
}

// ValidClineSessionID reports whether sessionID is a safe, valid Cline
// session ID that does not escape directories, traverse paths, or contain
// injection characters.
func ValidClineSessionID(sessionID string) bool {
	if sessionID == "" || strings.HasPrefix(sessionID, "_") || strings.HasPrefix(sessionID, ".") ||
		strings.ContainsAny(sessionID, "\\/:\x00") || !isSafeSinglePathComponent(sessionID) {
		return false
	}
	return true
}

// IsClineTeammateMessagesFile reports whether filename represents a Cline
// teammate subagent transcript (e.g. "<agentId>__<suffix>.messages.json")
// belonging to the session directory.
func IsClineTeammateMessagesFile(sessionID, filename string) bool {
	if !strings.HasSuffix(filename, ".messages.json") {
		return false
	}
	if filename == sessionID+".messages.json" {
		return false
	}
	base := strings.TrimSuffix(filename, ".messages.json")
	if base == "" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") ||
		strings.ContainsAny(base, "\\/:\x00") || !isSafeSinglePathComponent(base) {
		return false
	}
	idx := strings.Index(base, "__")
	if idx <= 0 || idx+2 >= len(base) || strings.HasSuffix(base, "__") {
		return false
	}
	return true
}

// ClineResolveSessionsDir resolves the directory containing Cline session
// folders. If root is already a direct sessions directory (named "sessions"
// or ending with "data/sessions"), root is returned; otherwise, "data/sessions"
// under root is returned.
func ClineResolveSessionsDir(root string) string {
	clean := filepath.Clean(root)
	if strings.HasSuffix(filepath.ToSlash(clean), "data/sessions") || filepath.Base(clean) == "sessions" {
		return clean
	}
	return filepath.Join(clean, "data", "sessions")
}

func clineResolveSessionsDir(root string) string {
	return ClineResolveSessionsDir(root)
}

func clineDiscoverEach(
	ctx context.Context, root string, yield func(singleFileMatch) error,
) error {
	sessionsDir := clineResolveSessionsDir(root)
	return streamDirectoryEntries(ctx, sessionsDir, func(entry os.DirEntry) error {
		if !entry.IsDir() || !ValidClineSessionID(entry.Name()) {
			return nil
		}
		sessionID := entry.Name()
		sessionDir := filepath.Join(sessionsDir, sessionID)
		if !clineSessionDirectoryWithinRoot(root, sessionDir, false) {
			return nil
		}
		metaPath := filepath.Join(sessionsDir, sessionID, sessionID+".json")
		info, err := clineRegularFileInfo(metaPath, true)
		if err != nil || info == nil {
			return nil
		}

		messagesPath := filepath.Join(
			filepath.Dir(metaPath), sessionID+".messages.json",
		)
		if _, err := clineRegularFileInfo(messagesPath, true); err != nil {
			return nil
		}
		return yield(singleFileMatch{Path: metaPath})
	})
}

// clineRegularFileInfo accepts only real regular files. Cline primary files
// must not be read through symlinks because the target is outside the source
// fingerprint and could otherwise be archived as an unrelated session.
func clineRegularFileInfo(path string, allowMissing bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("stat %s: source is not a regular file", path)
	}
	return info, nil
}

// clineSessionDirectoryWithinRoot validates the directory component that
// owns a Cline session. The lexical check prevents traversal, Lstat rejects a
// symlink at the session-directory boundary, and the resolved-path check
// prevents an intermediate symlink (for example data/) from escaping the
// configured Cline root. A missing session directory remains valid only for
// changed-path tombstone classification.
func clineSessionDirectoryWithinRoot(
	root, sessionDir string, allowMissing bool,
) bool {
	root = filepath.Clean(root)
	sessionsDir := clineResolveSessionsDir(root)
	sessionDir = filepath.Clean(sessionDir)
	if !isWithinRoot(sessionsDir, sessionDir) || sessionDir == sessionsDir {
		return false
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return allowMissing && os.IsNotExist(err)
	}

	info, err := os.Lstat(sessionDir)
	if os.IsNotExist(err) {
		return allowMissing
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	resolvedSessionDir, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return false
	}
	return isWithinRoot(resolvedRoot, resolvedSessionDir)
}

func clineWatchRoots(roots []string) []WatchRoot {
	out := make([]WatchRoot, 0, len(roots))
	for _, root := range roots {
		sessionsDir := clineResolveSessionsDir(root)
		out = append(out, WatchRoot{
			Path:         sessionsDir,
			Recursive:    true,
			IncludeGlobs: []string{"*.json"},
			DebounceKey:  "cline:sessions:" + root,
		})
	}
	return out
}

func clineClassifyPath(
	root, path string, allowMissing bool,
) (singleFileMatch, bool) {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	sessionsDir := clineResolveSessionsDir(root)

	rel, err := filepath.Rel(sessionsDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return singleFileMatch{}, false
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 {
		return singleFileMatch{}, false
	}

	sessionID := parts[0]
	filename := parts[1]

	if !ValidClineSessionID(sessionID) {
		return singleFileMatch{}, false
	}
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if !clineSessionDirectoryWithinRoot(root, sessionDir, allowMissing) {
		return singleFileMatch{}, false
	}

	if filename != sessionID+".json" && filename != sessionID+".messages.json" && !IsClineTeammateMessagesFile(sessionID, filename) {
		return singleFileMatch{}, false
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return singleFileMatch{}, false
		}
	} else if !os.IsNotExist(err) {
		return singleFileMatch{}, false
	}

	metaPath := filepath.Join(sessionsDir, sessionID, sessionID+".json")
	if allowMissing {
		if _, err := clineRegularFileInfo(metaPath, true); err != nil {
			return singleFileMatch{}, false
		}
		return singleFileMatch{Path: metaPath}, true
	}
	if _, err := clineRegularFileInfo(metaPath, false); err == nil {
		return singleFileMatch{Path: metaPath}, true
	}
	return singleFileMatch{}, false
}

// clineStoredSourceHintScope is the single-file stored-source-hint-scope hook
// for Cline: for any changed metadata or teammate transcript path, classify
// the path (allowing missing tombstone paths), resolve the owning session
// directory, and return it as the complete-result ownership scope. Teammate
// paths are ordinary descendants of the session directory, not path#member
// virtual paths, so IncludeVirtualMembers stays false.
func clineStoredSourceHintScope(root, path string) (StoredSourceHintScope, bool) {
	match, ok := clineClassifyPath(root, path, true)
	if !ok {
		return StoredSourceHintScope{}, false
	}
	return StoredSourceHintScope{
		Path:                  filepath.Dir(filepath.Clean(match.Path)),
		IncludeVirtualMembers: false,
	}, true
}

// clineFindFile resolves a Cline raw session ID (parent, __teamtask__ teammate,
// __teammate__ stable, __teammate__<subagent>__rid-<digest>, or legacy
// __run<number>) back to the owning session metadata file.
func clineFindFile(root, rawID string) (singleFileMatch, bool) {
	if !isSafeSinglePathComponent(rawID) || strings.ContainsAny(rawID, "\\/:\x00") {
		return singleFileMatch{}, false
	}
	sessionID := rawID
	if before, sub, found := strings.Cut(rawID, "__teammate__"); found {
		sessionID = before
		if sub == "" {
			return singleFileMatch{}, false
		}
		parts := strings.Split(sub, "__")
		if len(parts) == 0 || !isValidClineTeammateSubagentName(parts[0]) {
			return singleFileMatch{}, false
		}
		if len(parts) == 2 {
			suffix := parts[1]
			if !strings.HasPrefix(suffix, "run") && !strings.HasPrefix(suffix, "rid-") {
				return singleFileMatch{}, false
			}
			if !isValidClineTeammateSubagentName(suffix) {
				return singleFileMatch{}, false
			}
		} else if len(parts) > 2 {
			return singleFileMatch{}, false
		}
	} else if before, sub, found := strings.Cut(rawID, "__teamtask__"); found {
		sessionID = before
		if sub == "" || !isSafeSinglePathComponent(sub) {
			return singleFileMatch{}, false
		}
	}
	if !ValidClineSessionID(sessionID) {
		return singleFileMatch{}, false
	}
	sessionsDir := clineResolveSessionsDir(root)
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if !clineSessionDirectoryWithinRoot(root, sessionDir, false) {
		return singleFileMatch{}, false
	}
	metaPath := filepath.Join(sessionDir, sessionID+".json")
	if !isWithinRoot(sessionsDir, metaPath) {
		return singleFileMatch{}, false
	}
	if _, err := clineRegularFileInfo(metaPath, false); err == nil {
		return singleFileMatch{Path: metaPath}, true
	}
	return singleFileMatch{}, false
}

func clineParseFile(
	src singleFileSource, req ParseRequest,
) ([]ParseResult, []string, error) {
	if _, ok := clineClassifyPath(src.Root, src.Path, false); !ok {
		return nil, nil, fmt.Errorf(
			"cline source is outside the configured session root: %s", src.Path,
		)
	}
	results, err := parseClineSessionWithTeammates(
		src.Path, req.Source.ProjectHint, req.Machine, req.StoredSessionIDHints,
	)
	if err != nil {
		return nil, nil, err
	}
	if len(results) == 0 {
		return nil, nil, nil
	}

	if req.Fingerprint.Size > 0 {
		results[0].Session.File.Size = req.Fingerprint.Size
	}
	if req.Fingerprint.MTimeNS > 0 {
		results[0].Session.File.Mtime = req.Fingerprint.MTimeNS
	}
	if req.Fingerprint.Hash != "" {
		for i := range results {
			results[i].Session.File.Hash = req.Fingerprint.Hash
		}
	}

	return results, nil, nil
}

func clineProviderCapabilities() Capabilities {
	sourceCaps := jsonlFileProviderSourceCapabilities()
	sourceCaps.StoredSourceHints = CapabilitySupported
	return Capabilities{
		Source: sourceCaps,
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			GitBranch:            CapabilitySupported,
			Thinking:             CapabilitySupported,
			Model:                CapabilitySupported,
			ToolCalls:            CapabilitySupported,
			ToolResults:          CapabilitySupported,
			ToolResultEvents:     CapabilitySupported,
			PerMessageTokenUsage: CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			TerminationStatus:    CapabilitySupported,
			MalformedLineCount:   CapabilityNotApplicable,
		},
	}
}
