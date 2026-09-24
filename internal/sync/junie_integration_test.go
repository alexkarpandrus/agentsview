package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/parser"
)

func TestSyncJunieMetadataFreshnessAndSourceDeletion(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-one")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(eventsPath, []byte(
		`{"kind":"UserPromptEvent","requestId":"req-1","prompt":"Hello","timestampMs":1704067200500}`+"\n"+
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"response-1","text":"Draft response"}},"timestampMs":1704067200600}`+"\n"+
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":"JUNIE","modelUsage":[{"model":"claude-sonnet-4-6","cost":0.00125,"inputTokens":100,"cacheInputTokens":20,"cacheCreateTokens":30,"outputTokens":40,"time":1}]}},"timestampMs":1704067200750}`+"\n",
	), 0o600))

	indexPath := filepath.Join(root, "index.jsonl")
	beforeIndex := `{"sessionId":"session-one","projectDir":"/work/old","taskName":"before","createdAt":1704067200000,"updatedAt":1704067201000}` + "\n"
	afterIndex := `{"sessionId":"session-one","projectDir":"/work/new","taskName":"after!","createdAt":1704067200000,"updatedAt":1704067201000}` + "\n"
	require.Len(t, afterIndex, len(beforeIndex))
	require.NoError(t, os.WriteFile(indexPath, []byte(beforeIndex), 0o600))

	database := openTestDB(t)
	engine := NewEngine(t.Context(), database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentJunie: {root}},
		Machine:   "test",
	})
	t.Cleanup(engine.Close)

	first := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 1, first.Synced)
	require.Zero(t, first.Failed)

	unchanged := engine.SyncAll(t.Context(), nil)
	require.Zero(t, unchanged.Synced)
	require.Equal(t, 1, unchanged.Skipped)

	beforeStat, err := os.Stat(indexPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, []byte(afterIndex), 0o600))
	require.NoError(t, os.Chtimes(indexPath, beforeStat.ModTime(), beforeStat.ModTime()))
	afterStat, err := os.Stat(indexPath)
	require.NoError(t, err)
	require.Equal(t, beforeStat.Size(), afterStat.Size())
	require.Equal(t, beforeStat.ModTime(), afterStat.ModTime())
	eventsFile, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = eventsFile.WriteString(
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"response-1","text":"Final response"}},"timestampMs":1704067200900}` + "\n",
	)
	require.NoError(t, err)
	require.NoError(t, eventsFile.Close())

	updated := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 1, updated.Synced)
	require.Zero(t, updated.Failed)

	sess, err := database.GetSessionFull(t.Context(), "junie:session-one")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "new", sess.Project)
	require.NotNil(t, sess.SessionName)
	assert.Equal(t, "after!", *sess.SessionName)

	messages, err := database.GetMessages(t.Context(), "junie:session-one", 0, 100, true)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "Hello", messages[0].Content)
	assert.Equal(t, "Final response", messages[1].Content)
	usage, err := database.GetUsageEvents(t.Context(), "junie:session-one")
	require.NoError(t, err)
	require.Len(t, usage, 1)
	assert.Equal(t, "claude-sonnet-4-6", usage[0].Model)
	assert.Equal(t, 100, usage[0].InputTokens)
	assert.Equal(t, 40, usage[0].OutputTokens)
	assert.Equal(t, 20, usage[0].CacheReadInputTokens)
	assert.Equal(t, 30, usage[0].CacheCreationInputTokens)
	require.NotNil(t, usage[0].Cost)
	assert.Equal(t, int64(1_250), usage[0].Cost.Microdollars)

	unchanged = engine.SyncAll(t.Context(), nil)
	require.Zero(t, unchanged.Synced)
	require.Equal(t, 1, unchanged.Skipped)

	require.NoError(t, os.Remove(eventsPath))
	require.NoError(t, engine.ReconcileProviderRoots(
		t.Context(), parser.AgentJunie, []string{root},
	))

	archived, err := database.GetSessionFull(t.Context(), "junie:session-one")
	require.NoError(t, err)
	require.NotNil(t, archived)
	assertSourceMissingState(t, archived)
	messages, err = database.GetMessages(t.Context(), "junie:session-one", 0, 100, true)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "Hello", messages[0].Content)
	assert.Equal(t, "Final response", messages[1].Content)
	usage, err = database.GetUsageEvents(t.Context(), "junie:session-one")
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.NotNil(t, usage[0].Cost)
	assert.Equal(t, int64(1_250), usage[0].Cost.Microdollars)
}
