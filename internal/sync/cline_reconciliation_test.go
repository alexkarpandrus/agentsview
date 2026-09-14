package sync

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

// TestClineReconciliation_DeletedMetadataLifecycle verifies that deleting the
// owning metadata file still reaches source-missing reconciliation for the
// parent and every teammate result in its session directory.
func TestClineReconciliation_DeletedMetadataLifecycle(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-metadata")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-metadata.json")
	metaJSON := `{"session_id":"sess-metadata","cwd":"/workspace","started_at":"2026-09-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	msgPath := filepath.Join(sessDir, "sess-metadata.messages.json")
	msgJSON := `{"version":1,"messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"start"}],"ts":1000}]}`
	require.NoError(t, os.WriteFile(msgPath, []byte(msgJSON), 0o644))

	tmPath := filepath.Join(sessDir, "scout__t1.messages.json")
	tmJSON := `{"version":1,"sessionId":"sess-metadata__teamtask__scout__t1","origin":{"subagent":"scout"},"messages":[{"id":"tm1","role":"user","content":[{"type":"text","text":"scout task"}],"ts":1050}]}`
	require.NoError(t, os.WriteFile(tmPath, []byte(tmJSON), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCline: {root},
		},
		Machine: "test-machine",
	})
	t.Cleanup(engine.Close)

	first := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 2, first.Synced)

	parentID := "cline:sess-metadata"
	teammateID := "cline:sess-metadata__teammate__scout"
	for _, id := range []string{parentID, teammateID} {
		session, err := database.GetSessionFull(t.Context(), id)
		require.NoError(t, err)
		require.NotNil(t, session)
		assert.Nil(t, session.SourceMissingAt)
		assert.Nil(t, session.DeletedAt)
	}

	require.NoError(t, os.Remove(metaPath))
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{metaPath}))

	for _, id := range []string{parentID, teammateID} {
		session, err := database.GetSessionFull(t.Context(), id)
		require.NoError(t, err)
		require.NotNil(t, session, "session %s should remain archived", id)
		assertSourceMissingState(t, session)
	}

	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{metaPath}))

	for _, id := range []string{parentID, teammateID} {
		session, err := database.GetSessionFull(t.Context(), id)
		require.NoError(t, err)
		require.NotNil(t, session)
		assert.Nil(t, session.SourceMissingAt)
		assert.Nil(t, session.DeletedAt)
	}
}

func TestClineReconciliation_ContinuationAppearsAfterRootRemoval(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-late-continuation")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-late-continuation.json")
	require.NoError(t, os.WriteFile(
		metaPath,
		[]byte(`{"session_id":"sess-late-continuation","cwd":"/workspace"}`),
		0o644,
	))

	aRootPath := filepath.Join(sessDir, "worker__a_root.messages.json")
	bRootPath := filepath.Join(sessDir, "worker__b_root.messages.json")
	require.NoError(t, os.WriteFile(aRootPath, []byte(`{
		"sessionId":"sess-late-continuation__teamtask__worker__a_root",
		"origin":{"subagent":"worker"},
		"messages":[{"id":"a1","role":"user","content":[{"type":"text","text":"a"}],"ts":1000}]
	}`), 0o644))
	require.NoError(t, os.WriteFile(bRootPath, []byte(`{
		"sessionId":"sess-late-continuation__teamtask__worker__b_root",
		"origin":{"subagent":"worker"},
		"messages":[{"id":"b1","role":"user","content":[{"type":"text","text":"b"}],"ts":2000}]
	}`), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentCline: {root}},
		Machine:   "test-machine",
	})
	t.Cleanup(engine.Close)

	first := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 3, first.Synced)
	bIDs, err := database.ListSessionIDsByFilePath(bRootPath, string(parser.AgentCline))
	require.NoError(t, err)
	require.Len(t, bIDs, 1)
	stableBID := bIDs[0]

	// The continuation is first observed only after its root snapshot has been
	// removed. The archive's old file-path hint is the identity bridge.
	require.NoError(t, os.Remove(bRootPath))
	bContinuationPath := filepath.Join(sessDir, "worker__b_continuation.messages.json")
	require.NoError(t, os.WriteFile(bContinuationPath, []byte(`{
		"sessionId":"sess-late-continuation__teamtask__worker__b_continuation",
		"origin":{"subagent":"worker"},
		"messages":[
			{"id":"b1","role":"user","content":[{"type":"text","text":"b"}],"ts":2000},
			{"id":"b2","role":"assistant","content":[{"type":"text","text":"continued"}],"ts":3000}
		]
	}`), 0o644))
	now := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now, now))

	second := engine.SyncAll(t.Context(), nil)
	require.Greater(t, second.Synced, 0)
	continued, err := database.GetSessionFull(t.Context(), stableBID)
	require.NoError(t, err)
	require.NotNil(t, continued)
	assert.Nil(t, continued.SourceMissingAt)
	assert.Nil(t, continued.DeletedAt)
	require.NotNil(t, continued.FilePath)
	assert.Equal(t, bContinuationPath, *continued.FilePath)
	continuedIDs, err := database.ListSessionIDsByFilePath(
		bContinuationPath, string(parser.AgentCline),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{stableBID}, continuedIDs)
}

func TestClineRemoteIdentityStableAcrossResyncs(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-remote")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-remote.json")
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"session_id":"sess-remote","cwd":"/workspace"}`), 0o644))
	parentPath := filepath.Join(sessDir, "sess-remote.messages.json")
	parentJSON := `{"messages":[
		{"id":"p1","role":"assistant","content":[{"type":"tool_use","id":"run-worker","name":"team_run_task","input":{"agentId":"worker","taskId":"task-1"}}],"ts":1000},
		{"id":"p2","role":"user","content":[{"type":"tool_result","tool_use_id":"run-worker","content":"done"}],"ts":2000}
	]}`
	require.NoError(t, os.WriteFile(parentPath, []byte(parentJSON), 0o644))
	teammatePath := filepath.Join(sessDir, "worker__remote.messages.json")
	teammateJSON := func(text string) string {
		return `{"sessionId":"sess-remote__teamtask__worker__remote","origin":{"subagent":"worker"},"messages":[{"id":"t1","role":"user","content":[{"type":"text","text":"` + text + `"}],"ts":1100}]}`
	}
	require.NoError(t, os.WriteFile(teammatePath, []byte(teammateJSON("first")), 0o644))

	logicalRoot := "remote:/cline"
	rewrite := func(path string) string {
		if strings.HasPrefix(path, logicalRoot+"/") {
			return path
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return path
		}
		return logicalRoot + "/" + filepath.ToSlash(rel)
	}
	resolve := func(path string) (string, bool) {
		rel, ok := strings.CutPrefix(path, logicalRoot+"/")
		if !ok {
			return "", false
		}
		return filepath.Join(root, filepath.FromSlash(rel)), true
	}

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentCline: {root}},
		Machine:   "remote-host", IDPrefix: "remote-host~",
		PathRewriter: rewrite, StoredPathResolver: resolve,
	})
	t.Cleanup(engine.Close)

	parentID := "remote-host~cline:sess-remote"
	childID := "remote-host~cline:sess-remote__teammate__worker"
	readIdentity := func() (string, string, string, string) {
		t.Helper()
		parent, err := database.GetSessionFull(t.Context(), parentID)
		require.NoError(t, err)
		require.NotNil(t, parent)
		messages, err := database.GetAllMessages(t.Context(), parentID)
		require.NoError(t, err)
		var link, resultLink string
		for _, message := range messages {
			for _, call := range message.ToolCalls {
				if call.ToolName == "team_run_task" {
					link = call.SubagentSessionID
					if len(call.ResultEvents) == 1 {
						resultLink = call.ResultEvents[0].SubagentSessionID
					}
				}
			}
		}
		return parent.ID, childID, link, resultLink
	}

	first := engine.SyncAll(t.Context(), nil)
	require.Greater(t, first.Synced, 0)
	wantParent, wantChild, wantLink, wantResultLink := readIdentity()
	assert.Equal(t, parentID, wantParent)
	assert.Equal(t, childID, wantChild)
	assert.Equal(t, childID, wantLink)
	assert.Equal(t, childID, wantResultLink)

	for _, text := range []string{"second", "third"} {
		require.NoError(t, os.WriteFile(teammatePath, []byte(teammateJSON(text)), 0o644))
		res := engine.SyncAll(t.Context(), nil)
		require.Greater(t, res.Synced, 0)
		gotParent, gotChild, gotLink, gotResultLink := readIdentity()
		assert.Equal(t, wantParent, gotParent)
		assert.Equal(t, wantChild, gotChild)
		assert.Equal(t, wantLink, gotLink)
		assert.Equal(t, wantResultLink, gotResultLink)
	}

	storedPath := rewrite(teammatePath)
	ids, err := database.ListSessionIDsByFilePath(storedPath, string(parser.AgentCline))
	require.NoError(t, err)
	assert.Equal(t, []string{childID}, ids)
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

// TestClineReconciliation_ContinuationAndLifecycleRegression verifies the full
// 5-phase continuation, ordering, and deletion lifecycle:
//
//	Phase 1: Two distinct chains A (bare) and B (rid) are synced.
//	Phase 2: Continuation replaces Chain B's winner; emitted ID is unchanged,
//	         deriving from original root, and source path updates to continuation.
//	Phase 3: New distinct run sorts first; A and B remain unchanged, C gets
//	         new rid ID without stealing bare ID.
//	Phase 4: Delete Chain B live transcripts -> tombstoned via source_missing_at
//	         (deleted_at is nil); restore -> revived (source_missing_at is nil).
//	Phase 5: Delete chain root while continuation remains; continuation keeps
//	         same stable ID via persisted hint.
func TestClineReconciliation_ContinuationAndLifecycleRegression(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "data", "sessions", "sess-regression")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	metaPath := filepath.Join(sessDir, "sess-regression.json")
	metaJSON := `{"session_id":"sess-regression","cwd":"/workspace","started_at":"2026-09-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(metaPath, []byte(metaJSON), 0o644))

	msgPath := filepath.Join(sessDir, "sess-regression.messages.json")
	msgJSON := `{"version":1,"messages":[{"id":"m1","role":"user","content":[{"type":"text","text":"parent"}],"ts":1000}]}`
	require.NoError(t, os.WriteFile(msgPath, []byte(msgJSON), 0o644))

	// Chain A: root with 2 messages
	runAJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T10:00:00.000Z",
		"sessionId": "sess-regression__teamtask__worker__a_root",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "m_a1", "role": "user", "content": [{"type": "text", "text": "task A"}], "ts": 1000},
			{"id": "m_a2", "role": "assistant", "content": [{"type": "text", "text": "reply A"}], "ts": 1100}
		]
	}`
	aRootPath := filepath.Join(sessDir, "worker__a_root.messages.json")
	require.NoError(t, os.WriteFile(aRootPath, []byte(runAJSON), 0o644))

	// Chain B: root with 2 messages
	runBJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T10:00:00.000Z",
		"sessionId": "sess-regression__teamtask__worker__b_root",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "m_b1", "role": "user", "content": [{"type": "text", "text": "task B"}], "ts": 2000},
			{"id": "m_b2", "role": "assistant", "content": [{"type": "text", "text": "reply B"}], "ts": 2100}
		]
	}`
	bRootPath := filepath.Join(sessDir, "worker__b_root.messages.json")
	require.NoError(t, os.WriteFile(bRootPath, []byte(runBJSON), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := NewEngine(database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCline: {root},
		},
		Machine: "test-machine",
	})
	t.Cleanup(engine.Close)

	idA := "cline:sess-regression__teammate__worker"
	idB := "cline:sess-regression__teammate__worker__rid-c8592ba73b31efccc8bb2d39af84284d"
	idC := "cline:sess-regression__teammate__worker__rid-be9249151670be1ecb0349d80a289c3f"

	// --- Phase 1: Two distinct chains A (bare) and B (rid) ---
	res1 := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 3, res1.Synced)

	sessA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	require.NotNil(t, sessA)
	assert.Nil(t, sessA.SourceMissingAt)
	assert.Nil(t, sessA.DeletedAt)
	require.NotNil(t, sessA.FilePath)
	assert.Equal(t, aRootPath, *sessA.FilePath)

	sessB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	require.NotNil(t, sessB)
	assert.Nil(t, sessB.SourceMissingAt)
	assert.Nil(t, sessB.DeletedAt)
	require.NotNil(t, sessB.FilePath)
	assert.Equal(t, bRootPath, *sessB.FilePath)
	assert.Equal(t, 2, sessB.MessageCount)

	// --- Phase 2: Continuation replaces Chain B's winner ---
	// Continuation has 4 messages (prefix superset of b_root) and later updated_at
	runBContJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T11:00:00.000Z",
		"sessionId": "sess-regression__teamtask__worker__b_cont",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "m_b1", "role": "user", "content": [{"type": "text", "text": "task B"}], "ts": 2000},
			{"id": "m_b2", "role": "assistant", "content": [{"type": "text", "text": "reply B"}], "ts": 2100},
			{"id": "m_b3", "role": "user", "content": [{"type": "text", "text": "cont B"}], "ts": 2200},
			{"id": "m_b4", "role": "assistant", "content": [{"type": "text", "text": "reply cont B"}], "ts": 2300}
		]
	}`
	bContPath := filepath.Join(sessDir, "worker__b_cont.messages.json")
	require.NoError(t, os.WriteFile(bContPath, []byte(runBContJSON), 0o644))

	now := time.Now().Add(5 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now, now))

	res2 := engine.SyncAll(t.Context(), nil)
	require.True(t, res2.Synced > 0)

	sessBAfterCont, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	require.NotNil(t, sessBAfterCont)
	assert.Nil(t, sessBAfterCont.SourceMissingAt)
	assert.Nil(t, sessBAfterCont.DeletedAt)
	require.NotNil(t, sessBAfterCont.FilePath)
	assert.Equal(t, bContPath, *sessBAfterCont.FilePath, "source path must update to continuation")
	assert.Equal(t, 4, sessBAfterCont.MessageCount, "message count must reflect continuation winner")

	sessAAfterCont, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, sessAAfterCont.SourceMissingAt)
	require.NotNil(t, sessAAfterCont.FilePath)
	assert.Equal(t, aRootPath, *sessAAfterCont.FilePath)

	// --- Phase 3: New distinct run sorts first ---
	// Chain C has 5 messages, so it sorts ahead of Chain B (4 msgs) and Chain A (2 msgs)
	// in winner ranking (isBetterTeammateCandidate), but stored hints prevent it from
	// stealing the bare ID or reordering existing stable IDs.
	runCJSON := `{
		"version": 1,
		"updated_at": "2026-09-12T17:00:00.000Z",
		"sessionId": "sess-regression__teamtask__worker__c_first",
		"origin": {"subagent": "worker"},
		"messages": [
			{"id": "m_c1", "role": "user", "content": [{"type": "text", "text": "task C"}], "ts": 500},
			{"id": "m_c2", "role": "assistant", "content": [{"type": "text", "text": "reply C 1"}], "ts": 510},
			{"id": "m_c3", "role": "user", "content": [{"type": "text", "text": "more C"}], "ts": 520},
			{"id": "m_c4", "role": "assistant", "content": [{"type": "text", "text": "reply C 2"}], "ts": 530},
			{"id": "m_c5", "role": "assistant", "content": [{"type": "text", "text": "reply C 3"}], "ts": 540}
		]
	}`
	cFirstPath := filepath.Join(sessDir, "worker__c_first.messages.json")
	require.NoError(t, os.WriteFile(cFirstPath, []byte(runCJSON), 0o644))

	now2 := time.Now().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now2, now2))

	res3 := engine.SyncAll(t.Context(), nil)
	require.True(t, res3.Synced > 0)

	// A and B remain unchanged
	afterCA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, afterCA.SourceMissingAt)
	assert.Nil(t, afterCA.DeletedAt)

	afterCB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	assert.Nil(t, afterCB.SourceMissingAt)
	assert.Nil(t, afterCB.DeletedAt)

	// C gets new rid ID
	sessC, err := database.GetSessionFull(t.Context(), idC)
	require.NoError(t, err)
	require.NotNil(t, sessC)
	assert.Nil(t, sessC.SourceMissingAt)
	assert.Nil(t, sessC.DeletedAt)
	require.NotNil(t, sessC.FilePath)
	assert.Equal(t, cFirstPath, *sessC.FilePath)
	assert.Equal(t, 5, sessC.MessageCount)

	// --- Phase 4: Delete Chain B live transcripts -> tombstoned; restore -> revived ---
	require.NoError(t, os.Remove(bContPath))
	require.NoError(t, os.Remove(bRootPath))

	now3 := time.Now().Add(15 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now3, now3))

	res4 := engine.SyncAll(t.Context(), nil)
	require.True(t, res4.Synced > 0)

	archivedB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	assertSourceMissingState(t, archivedB)
	assert.Nil(t, archivedB.DeletedAt, "missing teammate transcript must not hard-delete session")

	// Chain A and C remain active
	activeA, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, activeA.SourceMissingAt)
	activeC, err := database.GetSessionFull(t.Context(), idC)
	require.NoError(t, err)
	assert.Nil(t, activeC.SourceMissingAt)

	// Restore Chain B files -> revived
	require.NoError(t, os.WriteFile(bRootPath, []byte(runBJSON), 0o644))
	require.NoError(t, os.WriteFile(bContPath, []byte(runBContJSON), 0o644))

	now4 := time.Now().Add(20 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now4, now4))

	res4Revive := engine.SyncAll(t.Context(), nil)
	require.True(t, res4Revive.Synced > 0)

	revivedB, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	require.NotNil(t, revivedB)
	assert.Nil(t, revivedB.SourceMissingAt, "source_missing_at must be cleared upon restore")
	assert.Nil(t, revivedB.DeletedAt)

	// --- Phase 5: Delete chain root while continuation remains ---
	require.NoError(t, os.Remove(bRootPath))

	now5 := time.Now().Add(25 * time.Second)
	require.NoError(t, os.Chtimes(metaPath, now5, now5))

	res5 := engine.SyncAll(t.Context(), nil)
	require.True(t, res5.Synced > 0)

	// Continuation keeps the same stable ID via persisted hint
	sessBAfterRootGone, err := database.GetSessionFull(t.Context(), idB)
	require.NoError(t, err)
	require.NotNil(t, sessBAfterRootGone)
	assert.Nil(t, sessBAfterRootGone.SourceMissingAt, "continuation must remain active via stored hint")
	assert.Nil(t, sessBAfterRootGone.DeletedAt)
	require.NotNil(t, sessBAfterRootGone.FilePath)
	assert.Equal(t, bContPath, *sessBAfterRootGone.FilePath)

	// Chains A and C remain active
	activeA5, err := database.GetSessionFull(t.Context(), idA)
	require.NoError(t, err)
	assert.Nil(t, activeA5.SourceMissingAt)
	activeC5, err := database.GetSessionFull(t.Context(), idC)
	require.NoError(t, err)
	assert.Nil(t, activeC5.SourceMissingAt)
}
