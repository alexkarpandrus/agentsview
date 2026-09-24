package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseJunieSession(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.jsonl"), []byte(
		`{"sessionId":"other","createdAt":1}`+"\n"+
			`{"sessionId":"`+sessionID+`","createdAt":1704067100000,"updatedAt":1704067109000,"projectDir":"/work/old","taskName":"Old title","status":"in_progress"}`+"\n"+
			`{"sessionId":"`+sessionID+`","createdAt":1704067200000,"updatedAt":1704067204500,"projectDir":"/work/demo","taskName":"Build the feature","status":"completed"}`+"\n",
	), 0o600))

	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"req-1","prompt":"hidden context","presentablePrompt":"Implement it","askMode":false,"thinkMore":false,"timestampMs":1704067201000}` + "\n" +
			`{"kind":"UserPromptEvent","requestId":"req-dropped","prompt":"Drop me","askMode":false,"thinkMore":false,"timestampMs":1704067202000}` + "\n" +
			`{"kind":"UserMessagesDroppedFromHistory","userMessageIds":["req-dropped"],"timestampMs":1704067202500}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"step-1","text":"thinking aloud"}},"timestampMs":1704067203000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-1","cancelled":false,"result":"draft","changes":[]}},"timestampMs":1704067204000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-1","cancelled":false,"result":"<!-- ANSWER -->Done","changes":[]}},"timestampMs":1704067205000}` + "\n" +
			`{"kind":"SystemMessageEvent","text":"Notice","details":"Details","level":"ERROR","symbol":"!","timestampMs":1704067205500}` + "\n" +
			`{"kind":"AgentTaskFailedEvent","timestampMs":1704067205600}` + "\n" +
			`{"kind":"UserAsyncResponseEvent","entries":[{"question":"Continue?","answer":"Yes"}],"timestampMs":1704067206000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-2","cancelled":false,"result":"Finished","changes":[]}},"timestampMs":1704067207000}` + "\n" +
			"{\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, messages, err := parseJunieSession(t.Context(), path, "local")
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "junie:"+sessionID, sess.ID)
	assert.Equal(t, sessionID, sess.SourceSessionID)
	assert.Equal(t, AgentJunie, sess.Agent)
	assert.Equal(t, "local", sess.Machine)
	assert.Equal(t, "demo", sess.Project)
	assert.Equal(t, "/work/demo", sess.Cwd)
	assert.Equal(t, "Build the feature", sess.FirstMessage)
	assert.Equal(t, "Build the feature", sess.SessionName)
	assert.True(t, sess.SessionNamePresent)
	assert.Equal(t, time.UnixMilli(1704067200000), sess.StartedAt)
	assert.Equal(t, time.UnixMilli(1704067207000), sess.EndedAt)
	assert.Equal(t, 6, sess.MessageCount)
	assert.Equal(t, 2, sess.UserMessageCount)
	assert.Equal(t, 1, sess.MalformedLines)

	require.Len(t, messages, 6)
	assertMessage(t, messages[0], RoleUser, "Implement it")
	assertMessage(t, messages[1], RoleAssistant, "Done")
	assertMessage(t, messages[2], RoleSystem, "Notice\n\nDetails")
	assertMessage(t, messages[3], RoleSystem, "Agent task failed")
	assert.True(t, messages[2].IsSystem)
	assert.True(t, messages[3].IsSystem)
	assertMessage(t, messages[4], RoleUser, "Continue?\nYes")
	assertMessage(t, messages[5], RoleAssistant, "Finished")
	for i, message := range messages {
		assert.Equal(t, i, message.Ordinal)
	}
	assert.Equal(t, time.UnixMilli(1704067205000), messages[1].Timestamp)
}

func TestJunieSourceSetDiscoversOnlyEventStreams(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "session-one", "events.jsonl"),
		filepath.Join(root, "session-two", "events.jsonl"),
		filepath.Join(root, "session-one", "state.json"),
		filepath.Join(root, "session-two", "transcript.md"),
		filepath.Join(root, "index.jsonl"),
		filepath.Join(root, "nested", "session-three", "events.jsonl"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	}
	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","projectDir":"/old-one"}`+"\n"+
			`{"sessionId":"session-two","projectDir":"/old-two"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{
		Roots:   []string{root},
		Machine: "local",
	})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 2)
	assert.Equal(t, AgentJunie, sources[0].Provider)
	assert.Equal(t, "session-one", sources[0].ProjectHint)
	assert.Equal(t, filepath.Join(root, "session-one", "events.jsonl"), sources[0].DisplayPath)
	assert.Equal(t, "session-two", sources[1].ProjectHint)
	plan, err := provider.WatchPlan(t.Context())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Contains(t, plan.Roots[0].IncludeGlobs, "index.jsonl")
	planner, ok := provider.(WatchRootPlanner)
	require.True(t, ok)
	watchRoots, err := planner.WatchRoots(t.Context())
	require.NoError(t, err)
	require.Len(t, watchRoots, 1)
	assert.Contains(t, watchRoots[0].IncludeGlobs, "index.jsonl")

	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	unchangedFingerprint, err := provider.Fingerprint(t.Context(), sources[1])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","projectDir":"/new-one"}`+"\n"+
			`{"sessionId":"session-two","projectDir":"/old-two"}`+"\n",
	), 0o600))

	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      indexPath,
		WatchRoot: root,
	})
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, sources[0].Key, changed[0].Key)

	updatedFingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	assert.NotEqual(t, fingerprint.Hash, updatedFingerprint.Hash)
	stillUnchangedFingerprint, err := provider.Fingerprint(t.Context(), sources[1])
	require.NoError(t, err)
	assert.Equal(t, unchangedFingerprint, stillUnchangedFingerprint)

	require.NoError(t, os.Remove(indexPath))
	changed, err = provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      indexPath,
		WatchRoot: root,
	})
	require.NoError(t, err)
	require.Len(t, changed, 2)
}

func TestJunieSourceSetReusesIndexSnapshotWhileParsing(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-one")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","prompt":"Hello","timestampMs":1704067201000}`+"\n",
	), 0o600))
	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","taskName":"Cached title"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.NoError(t, os.Remove(indexPath))

	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: sources[0]})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Equal(t, "Cached title", outcome.Results[0].Result.Session.SessionName)
}

func TestJunieIndexChangeWorkIsBoundedByChangedSessions(t *testing.T) {
	for _, sessionCount := range []int{2, 128} {
		t.Run(fmt.Sprintf("sessions-%d", sessionCount), func(t *testing.T) {
			root := t.TempDir()
			changedID := fmt.Sprintf("session-%03d", sessionCount/2)
			var before, after strings.Builder
			for i := range sessionCount {
				sessionID := fmt.Sprintf("session-%03d", i)
				path := filepath.Join(root, sessionID, "events.jsonl")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
				fmt.Fprintf(&before, `{"sessionId":"%s","taskName":"old"}`+"\n", sessionID)
				title := "old"
				if sessionID == changedID {
					title = "new"
				}
				fmt.Fprintf(&after, `{"sessionId":"%s","taskName":"%s"}`+"\n", sessionID, title)
			}

			indexPath := filepath.Join(root, "index.jsonl")
			require.NoError(t, os.WriteFile(indexPath, []byte(before.String()), 0o600))
			sources := newJunieSourceSet([]string{root})
			_, err := sources.WatchPlan(t.Context())
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(indexPath, []byte(after.String()), 0o600))

			changed, err := sources.SourcesForChangedPath(t.Context(), ChangedPathRequest{
				Path:      indexPath,
				WatchRoot: root,
			})
			require.NoError(t, err)
			require.Len(t, changed, 1)
			assert.Equal(t, changedID, changed[0].ProjectHint)
		})
	}
}

func TestParseJunieSessionWithoutIndexUsesEventMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-fallback", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"SessionTitleSetEvent","name":"Fallback title","timestampMs":1704067200000}`+"\n"+
			`{"kind":"UserResponseEvent","prompt":"Yes","timestampMs":1704067201000}`+"\n",
	), 0o600))

	sess, messages, err := parseJunieSession(t.Context(), path, "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "junie", sess.Project)
	assert.Empty(t, sess.Cwd)
	assert.Equal(t, "Fallback title", sess.SessionName)
	assert.Equal(t, "Fallback title", sess.FirstMessage)
	assert.Equal(t, time.UnixMilli(1704067200000), sess.StartedAt)
	assert.Equal(t, time.UnixMilli(1704067201000), sess.EndedAt)
	require.Len(t, messages, 1)
	assertMessage(t, messages[0], RoleUser, "Yes")
}

func TestParseJunieSessionIgnoresSymlinkedIndex(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside-index.jsonl")
	require.NoError(t, os.WriteFile(target, []byte(
		`{"sessionId":"session-safe","projectDir":"/private/secret","taskName":"Secret"}`+"\n",
	), 0o600))
	if err := os.Symlink(target, filepath.Join(root, "index.jsonl")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	path := filepath.Join(root, "session-safe", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserResponseEvent","prompt":"Safe","timestampMs":1704067201000}`+"\n",
	), 0o600))

	sess, _, err := parseJunieSession(t.Context(), path, "local")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "junie", sess.Project)
	assert.Empty(t, sess.Cwd)
	assert.Empty(t, sess.SessionName)
}
