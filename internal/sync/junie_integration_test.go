package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/parser"
)

func TestSyncJunieSameSizeIndexMetadataUpdate(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-one")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"req-1","prompt":"Hello","timestampMs":1704067200500}`+"\n",
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
	require.Len(t, messages, 1)
	assert.Equal(t, "Hello", messages[0].Content)

	unchanged = engine.SyncAll(t.Context(), nil)
	require.Zero(t, unchanged.Synced)
	require.Equal(t, 1, unchanged.Skipped)
}
