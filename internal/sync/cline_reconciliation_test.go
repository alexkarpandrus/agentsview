package sync

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
)

// TestClineReconciliation_DeletedTeammateLifecycle verifies the full lifecycle
// of a deleted Cline teammate transcript:
//  1. Sync parent session and teammate transcript -> both active.
//  2. Delete teammate file and resync -> teammate is tombstoned via
//     source-missing, NOT hard-deleted into trash (deleted_at is nil).
//  3. Restore the file and resync -> teammate revives and clears source_missing_at.
func TestClineReconciliation_DeletedTeammateLifecycle(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-lifecycle")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-lifecycle.json")
	metaJSON := `{"session_id":"sess-lifecycle","cwd":"/workspace","started_at":"2026-09-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	msgPath := filepath.Join(sessDir, "sess-lifecycle.messages.json")
	msgJSON := `{"version":1,"messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"start"}],"ts":1000}]}`
	require.NoError(t, os.WriteFile(msgPath, []byte(msgJSON), 0o644))

	tmPath := filepath.Join(sessDir, "scout__t1.messages.json")
	tmJSON := `{"version":1,"sessionId":"sess-lifecycle__teamtask__scout__t1","origin":{"subagent":"scout"},"messages":[{"id":"tm1","role":"user","content":[{"type":"text","text":"scout task"}],"ts":1050}]}`
	require.NoError(t, os.WriteFile(tmPath, []byte(tmJSON), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCline: {root},
		},
		Machine: "test-machine",
	})
	t.Cleanup(engine.Close)

	// Step 1: Initial sync (parent + teammate)
	res := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 2, res.Synced)

	teammateID := "cline:sess-lifecycle__teammate__scout"
	sess, err := database.GetSessionFull(t.Context(), teammateID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Nil(t, sess.DeletedAt)
	assert.Nil(t, sess.SourceMissingAt)

	// Step 2: Delete teammate file and resync.
	require.NoError(t, os.Remove(tmPath))
	now := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now, now))

	res2 := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 1, res2.Synced)

	archived, err := database.GetSessionFull(t.Context(), teammateID)
	require.NoError(t, err)
	assertSourceMissingState(t, archived)
	assert.Nil(t, archived.DeletedAt, "missing teammate file must not put session into trash")

	// Step 3: Restore teammate file and resync -> session revives.
	require.NoError(t, os.WriteFile(tmPath, []byte(tmJSON), 0o644))
	now2 := time.Now().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now2, now2))

	res3 := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 2, res3.Synced)

	revived, err := database.GetSessionFull(t.Context(), teammateID)
	require.NoError(t, err)
	require.NotNil(t, revived)
	assert.Nil(t, revived.SourceMissingAt, "source_missing_at must be cleared when file returns")
	assert.Nil(t, revived.DeletedAt)
}

// TestClineReconciliation_LegacyRunTombstoned verifies that a legacy positional
// teammate ID (__run2) stored in the archive is tombstoned rather than hard-deleted
// when re-syncing under the stable root-derived ID system.
func TestClineReconciliation_LegacyRunTombstoned(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-mig")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-mig.json")
	metaJSON := `{"session_id":"sess-mig","cwd":"/workspace","started_at":"2026-09-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	msgPath := filepath.Join(sessDir, "sess-mig.messages.json")
	msgJSON := `{"version":1,"messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"parent"}],"ts":1000}]}`
	require.NoError(t, os.WriteFile(msgPath, []byte(msgJSON), 0o644))

	// Run 1: bare teammate
	run1JSON := `{"version":1,"sessionId":"sess-mig__teamtask__worker__r1","origin":{"subagent":"worker"},"messages":[{"id":"m1_1","role":"user","content":[{"type":"text","text":"task 1"}],"ts":1000}]}`
	r1Path := filepath.Join(sessDir, "worker__r1.messages.json")
	require.NoError(t, os.WriteFile(r1Path, []byte(run1JSON), 0o644))

	// Run 2: distinct run
	run2JSON := `{"version":1,"sessionId":"sess-mig__teamtask__worker__r2","origin":{"subagent":"worker"},"messages":[{"id":"m2_1","role":"user","content":[{"type":"text","text":"task 2"}],"ts":2000}]}`
	r2Path := filepath.Join(sessDir, "worker__r2.messages.json")
	require.NoError(t, os.WriteFile(r2Path, []byte(run2JSON), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCline: {root},
		},
		Machine: "test-machine",
	})
	t.Cleanup(engine.Close)

	// In the archive, simulate an older version having stored Run 2 under legacy __run2 ID
	legacyID := "cline:sess-mig__teammate__worker__run2"
	require.NoError(t, database.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO sessions (
				id, project, machine, agent, file_path, file_size, file_mtime,
				started_at, created_at, transcript_revision, data_version
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '0', ?)
		`, legacyID, "proj", "test-machine", "cline", r2Path, 100, 1000,
			"2026-09-12T10:00:00Z", "2026-09-12T10:00:00Z", db.CurrentDataVersion())
		if err != nil {
			return err
		}
		_, err = tx.Exec(`
			INSERT INTO local_session_source_baselines (session_id, machine, agent, file_path)
			VALUES (?, ?, ?, ?)
		`, legacyID, "test-machine", "cline", r2Path)
		return err
	}))

	// Verify legacy row is present and active before sync
	beforeSync, err := database.GetSessionFull(t.Context(), legacyID)
	require.NoError(t, err)
	require.NotNil(t, beforeSync)
	assert.Nil(t, beforeSync.SourceMissingAt)
	assert.Nil(t, beforeSync.DeletedAt)

	// Sync: the parser ignores __run2, derives new stable rid ID, and reconciles
	// the legacy row into source-missing.
	res := engine.SyncAll(t.Context(), nil)
	require.True(t, res.Synced > 0)

	// The legacy __run2 row must be tombstoned via source-missing, NOT hard-deleted
	legacyArchived, err := database.GetSessionFull(t.Context(), legacyID)
	require.NoError(t, err)
	assertSourceMissingState(t, legacyArchived)
	assert.Nil(t, legacyArchived.DeletedAt, "legacy __run2 row must not be hard-deleted")

	// And the new stable root-derived ID must exist and be active
	activeSessions, err := database.ListSessionIDsByFilePath(r2Path, string(parser.AgentCline))
	require.NoError(t, err)
	require.Len(t, activeSessions, 1)
	assert.Contains(t, activeSessions[0], "__rid-")
	assert.NotEqual(t, legacyID, activeSessions[0])
}

// TestClineReconciliation_ThreePhaseRegression verifies:
// Phase 1: two distinct runs produce IDs A (bare) and B (rid).
// Phase 2: add a third run that sorts first; A and B remain unchanged.
// Phase 3: delete run B and resync; verify run B becomes source-missing while
//
//	A and the third run remain active.
func TestClineReconciliation_ThreePhaseRegression(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-3phase")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-3phase.json")
	metaJSON := `{"session_id":"sess-3phase","cwd":"/workspace","started_at":"2026-09-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	msgPath := filepath.Join(sessDir, "sess-3phase.messages.json")
	msgJSON := `{"version":1,"messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"parent"}],"ts":1000}]}`
	require.NoError(t, os.WriteFile(msgPath, []byte(msgJSON), 0o644))

	// Run 1 (bare candidate)
	run1JSON := `{"version":1,"sessionId":"sess-3phase__teamtask__worker__r1","origin":{"subagent":"worker"},"messages":[{"id":"m1_1","role":"user","content":[{"type":"text","text":"task 1"}],"ts":1000}]}`
	r1Path := filepath.Join(sessDir, "worker__r1.messages.json")
	require.NoError(t, os.WriteFile(r1Path, []byte(run1JSON), 0o644))

	// Run 2 (rid candidate)
	run2JSON := `{"version":1,"sessionId":"sess-3phase__teamtask__worker__r2","origin":{"subagent":"worker"},"messages":[{"id":"m2_1","role":"user","content":[{"type":"text","text":"task 2"}],"ts":2000}]}`
	r2Path := filepath.Join(sessDir, "worker__r2.messages.json")
	require.NoError(t, os.WriteFile(r2Path, []byte(run2JSON), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCline: {root},
		},
		Machine: "test-machine",
	})
	t.Cleanup(engine.Close)

	// Phase 1: Sync produces A and B
	res1 := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 3, res1.Synced)

	idA := "cline:sess-3phase__teammate__worker"
	sessA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	require.NotNil(t, sessA)

	r2IDs, err := database.ListSessionIDsByFilePath(r2Path, string(parser.AgentCline))
	require.NoError(t, err)
	require.Len(t, r2IDs, 1)
	idB := r2IDs[0]
	assert.Contains(t, idB, "__rid-")

	// Phase 2: Add Run 0 which sorts before Run 1
	run0JSON := `{"version":1,"sessionId":"sess-3phase__teamtask__worker__r0","origin":{"subagent":"worker"},"messages":[{"id":"m0_1","role":"user","content":[{"type":"text","text":"task 0"}],"ts":500}]}`
	r0Path := filepath.Join(sessDir, "worker__r0.messages.json")
	require.NoError(t, os.WriteFile(r0Path, []byte(run0JSON), 0o644))

	now := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now, now))

	res2 := engine.SyncAll(t.Context(), nil)
	require.True(t, res2.Synced > 0)

	// Assert idA and idB remain unchanged
	afterA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, afterA.SourceMissingAt)
	assert.Nil(t, afterA.DeletedAt)

	afterB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	assert.Nil(t, afterB.SourceMissingAt)
	assert.Nil(t, afterB.DeletedAt)

	// And run 0 has its own stable ID
	r0IDs, err := database.ListSessionIDsByFilePath(r0Path, string(parser.AgentCline))
	require.NoError(t, err)
	require.Len(t, r0IDs, 1)
	id0 := r0IDs[0]
	assert.Contains(t, id0, "__rid-")
	assert.NotEqual(t, idB, id0)

	// Phase 3: Delete run B file and resync
	require.NoError(t, os.Remove(r2Path))
	now2 := time.Now().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now2, now2))

	res3 := engine.SyncAll(t.Context(), nil)
	require.True(t, res3.Synced > 0)

	// Verify run B is now source-missing, NOT hard-deleted
	archivedB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	assertSourceMissingState(t, archivedB)
	assert.Nil(t, archivedB.DeletedAt)

	// Verify run A and run 0 remain active
	activeA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, activeA.SourceMissingAt)

	active0, err := database.GetSessionFull(t.Context(), id0)
	require.NoError(t, err)
	assert.Nil(t, active0.SourceMissingAt)
}
