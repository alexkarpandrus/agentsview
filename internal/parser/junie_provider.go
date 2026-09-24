package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/tidwall/gjson"
)

type junieSourceSet struct {
	JSONLSourceSet
	indexCache *junieIndexCache
}

type junieIndexCache struct {
	mu        sync.Mutex
	summaries map[string]map[string]string
}

func newJunieProviderFactory(def AgentDef) ProviderFactory {
	return NewSourceSetFactory(
		def,
		junieProviderCapabilities(),
		func(cfg ProviderConfig) SourceSet { return newJunieSourceSet(cfg.Roots) },
	)
}

func newJunieSourceSet(roots []string) junieSourceSet {
	indexCache := &junieIndexCache{
		summaries: make(map[string]map[string]string),
	}
	return junieSourceSet{
		JSONLSourceSet: NewJSONLSourceSet(AgentJunie, roots,
			WithRecursive(),
			WithContentHashing(),
			WithIncludePath(func(root, path string) bool {
				return filepath.Base(path) == "events.jsonl" &&
					IsDirectoryJSONLPath(root, path)
			}),
			WithSessionIDFromPath(func(_, path string) string {
				return filepath.Base(filepath.Dir(path))
			}),
			WithProjectHint(func(_, path string) string {
				return filepath.Base(filepath.Dir(path))
			}),
			WithParseFile(indexCache.parseFile),
		),
		indexCache: indexCache,
	}
}

func (s junieSourceSet) Discover(ctx context.Context) ([]SourceRef, error) {
	return collectDiscoveredSources(ctx, s.DiscoverEach)
}

func (s junieSourceSet) DiscoverEach(
	ctx context.Context, yield func(SourceRef) error,
) error {
	if err := s.refreshJunieIndexes(ctx); err != nil {
		return err
	}
	return s.JSONLSourceSet.DiscoverEach(ctx, yield)
}

func (s junieSourceSet) WatchPlan(ctx context.Context) (WatchPlan, error) {
	plan, err := s.JSONLSourceSet.WatchPlan(ctx)
	if err != nil {
		return WatchPlan{}, err
	}
	for i := range plan.Roots {
		plan.Roots[i].IncludeGlobs = append(plan.Roots[i].IncludeGlobs, "index.jsonl")
	}
	if err := s.refreshJunieIndexes(ctx); err != nil {
		return WatchPlan{}, err
	}
	return plan, nil
}

func (s junieSourceSet) WatchRoots(ctx context.Context) ([]WatchRoot, error) {
	plan, err := s.WatchPlan(ctx)
	return plan.Roots, err
}

func (s junieSourceSet) SourcesForChangedPath(
	ctx context.Context, req ChangedPathRequest,
) ([]SourceRef, error) {
	if filepath.Base(req.Path) != "index.jsonl" {
		return s.JSONLSourceSet.SourcesForChangedPath(ctx, req)
	}

	var root, indexPath string
	for _, candidateRoot := range s.JSONLSourceSet.roots {
		candidateIndex := filepath.Join(candidateRoot, "index.jsonl")
		if samePath(req.Path, candidateIndex) {
			root, indexPath = candidateRoot, candidateIndex
			break
		}
	}
	if root == "" {
		return nil, nil
	}

	s.indexCache.mu.Lock()
	current, present, err := loadJunieIndexSnapshot(ctx, indexPath)
	if err != nil {
		s.indexCache.mu.Unlock()
		return nil, err
	}
	if !present {
		current = map[string]string{}
	}
	previous, known := s.indexCache.summaries[indexPath]
	s.indexCache.summaries[indexPath] = current
	s.indexCache.mu.Unlock()
	if !known {
		previous = map[string]string{}
	}

	changedIDs := changedJunieSummaryIDs(previous, current)
	sources := make([]SourceRef, 0, len(changedIDs))
	for _, sessionID := range changedIDs {
		path := filepath.Join(root, sessionID, "events.jsonl")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		source, ok := s.JSONLSourceSet.sourceRef(root, path, info)
		if ok {
			sources = append(sources, source)
		}
	}
	return sources, nil
}

func (s junieSourceSet) Fingerprint(
	ctx context.Context, source SourceRef,
) (SourceFingerprint, error) {
	fingerprint, err := s.JSONLSourceSet.Fingerprint(ctx, source)
	if err != nil {
		return SourceFingerprint{}, err
	}
	src, ok := source.Opaque.(JSONLSource)
	if !ok {
		return fingerprint, nil
	}
	indexPath := filepath.Join(filepath.Dir(filepath.Dir(src.Path)), "index.jsonl")
	summaries, err := s.indexCache.snapshot(ctx, indexPath)
	if err != nil {
		return SourceFingerprint{}, err
	}
	summary := summaries[filepath.Base(filepath.Dir(src.Path))]
	if summary == "" {
		return fingerprint, nil
	}

	// Scope shared index metadata to this session so one summary update does not
	// invalidate every transcript under the Junie root.
	sum := sha256.Sum256([]byte(fingerprint.Hash + "\x00" + summary))
	fingerprint.Hash = hex.EncodeToString(sum[:])
	fingerprint.Size += int64(len(summary))
	return fingerprint, nil
}

func changedJunieSummaryIDs(previous, current map[string]string) []string {
	changed := make([]string, 0)
	for sessionID, summary := range current {
		if previous[sessionID] != summary {
			changed = append(changed, sessionID)
		}
	}
	for sessionID := range previous {
		if _, present := current[sessionID]; !present {
			changed = append(changed, sessionID)
		}
	}
	sort.Strings(changed)
	return changed
}

func (s junieSourceSet) refreshJunieIndexes(ctx context.Context) error {
	for _, root := range s.JSONLSourceSet.roots {
		indexPath := filepath.Join(root, "index.jsonl")
		snapshot, present, err := loadJunieIndexSnapshot(ctx, indexPath)
		if err != nil {
			return err
		}
		if !present {
			snapshot = map[string]string{}
		}
		s.indexCache.mu.Lock()
		s.indexCache.summaries[indexPath] = snapshot
		s.indexCache.mu.Unlock()
	}
	return nil
}

func (c *junieIndexCache) snapshot(
	ctx context.Context, indexPath string,
) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot, loaded := c.summaries[indexPath]; loaded {
		return snapshot, nil
	}
	snapshot, present, err := loadJunieIndexSnapshot(ctx, indexPath)
	if err != nil {
		return nil, err
	}
	if !present {
		snapshot = map[string]string{}
	}
	c.summaries[indexPath] = snapshot
	return snapshot, nil
}

func loadJunieIndexSnapshot(
	ctx context.Context, path string,
) (map[string]string, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat Junie index %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, false, fmt.Errorf("open Junie index %s: %w", path, err)
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat Junie index %s: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, false, nil
	}

	summaries := make(map[string]string)
	lr := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(lr)
	lineNumber := 0
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		lineNumber++
		if err := contextErrEvery(ctx, lineNumber); err != nil {
			return nil, false, err
		}
		if !gjson.Valid(line) {
			continue
		}
		sessionID := gjson.Get(line, "sessionId").Str
		if sessionID != "" {
			summaries[sessionID] = string(line)
		}
	}
	if err := lr.Err(); err != nil {
		return nil, false, fmt.Errorf("reading Junie index %s: %w", path, err)
	}
	return summaries, true, nil
}

func (c *junieIndexCache) parseFile(
	ctx context.Context, path string, req ParseRequest,
) ([]ParseResult, []string, error) {
	sessionID := filepath.Base(filepath.Dir(path))
	indexPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "index.jsonl")
	summaries, err := c.snapshot(ctx, indexPath)
	if err != nil {
		return nil, nil, err
	}
	line, present := summaries[sessionID]
	summary := junieSessionSummary{}
	if present {
		summary = parseJunieSessionSummary(line)
	}
	sess, msgs, err := parseJunieSessionWithSummary(
		ctx, path, req.Machine, summary, present,
	)
	if err != nil {
		return nil, nil, err
	}
	if sess == nil {
		return nil, nil, nil
	}
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}
	if req.Fingerprint.Size > 0 {
		sess.File.Size = req.Fingerprint.Size
	}
	if req.Fingerprint.MTimeNS > 0 {
		sess.File.Mtime = req.Fingerprint.MTimeNS
	}
	return []ParseResult{{Session: *sess, Messages: msgs, UsageEvents: sess.UsageEvents}}, nil, nil
}

func junieProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Sync: ProviderSyncSemantics{
			FingerprintHashInCacheKey:           true,
			FingerprintHashRequiredForFreshness: true,
		},
		Content: ContentCapabilities{
			FirstMessage:         CapabilitySupported,
			SessionName:          CapabilitySupported,
			Cwd:                  CapabilitySupported,
			AggregateUsageEvents: CapabilitySupported,
			MalformedLineCount:   CapabilitySupported,
		},
	}
}
