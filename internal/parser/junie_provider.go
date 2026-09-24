package parser

import (
	"context"
	"path/filepath"
)

func newJunieProviderFactory(def AgentDef) ProviderFactory {
	return NewSourceSetFactory(
		def,
		junieProviderCapabilities(),
		func(cfg ProviderConfig) SourceSet { return newJunieSourceSet(cfg.Roots) },
	)
}

func newJunieSourceSet(roots []string) JSONLSourceSet {
	return NewJSONLSourceSet(AgentJunie, roots,
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
		WithParseFile(junieParseFile),
	)
}

func junieParseFile(
	ctx context.Context, path string, req ParseRequest,
) ([]ParseResult, []string, error) {
	sess, msgs, err := parseJunieSession(ctx, path, req.Machine)
	if err != nil {
		return nil, nil, err
	}
	if sess == nil {
		return nil, nil, nil
	}
	if req.Fingerprint.Hash != "" {
		sess.File.Hash = req.Fingerprint.Hash
	}
	return []ParseResult{{Session: *sess, Messages: msgs}}, nil, nil
}

func junieProviderCapabilities() Capabilities {
	return Capabilities{
		Source: jsonlFileProviderSourceCapabilities(),
		Content: ContentCapabilities{
			FirstMessage:       CapabilitySupported,
			SessionName:        CapabilitySupported,
			Cwd:                CapabilitySupported,
			MalformedLineCount: CapabilitySupported,
		},
	}
}
