package parser

import (
	"os"
	"path/filepath"
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
			`{"sessionId":"`+sessionID+`","createdAt":1704067200000,"updatedAt":1704067209000,"projectDir":"/work/demo","taskName":"Build the feature","status":"completed"}`+"\n",
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
	assert.Equal(t, time.UnixMilli(1704067209000), sess.EndedAt)
	assert.Equal(t, 6, sess.MessageCount)
	assert.Equal(t, 2, sess.UserMessageCount)
	assert.Equal(t, 1, sess.MalformedLines)

	require.Len(t, messages, 6)
	assertMessage(t, messages[0], RoleUser, "Implement it")
	assertMessage(t, messages[1], RoleAssistant, "Done")
	assertMessage(t, messages[2], RoleSystem, "Notice\n\nDetails")
	assertMessage(t, messages[3], RoleSystem, "Agent task failed")
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
		filepath.Join(root, "session-one", "state.json"),
		filepath.Join(root, "session-two", "transcript.md"),
		filepath.Join(root, "index.jsonl"),
		filepath.Join(root, "nested", "session-three", "events.jsonl"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	}

	provider, ok := NewProvider(AgentJunie, ProviderConfig{
		Roots:   []string{root},
		Machine: "local",
	})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, AgentJunie, sources[0].Provider)
	assert.Equal(t, "session-one", sources[0].ProjectHint)
	assert.Equal(t, filepath.Join(root, "session-one", "events.jsonl"), sources[0].DisplayPath)
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
