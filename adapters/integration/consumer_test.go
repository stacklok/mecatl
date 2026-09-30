package consumer_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/adapters/grpcdriver"
	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/adapters/redisstore"
	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// The reader and writer are separate production handles. The wire variant
// crosses both public server wrappers rather than substituting a port mock.
func TestExternalConsumer_PersistedReaderAndWriter(t *testing.T) {
	for _, backend := range []string{"jsonl", "redis", "grpc"} {
		t.Run(backend, func(t *testing.T) {
			var writer, reader port.SessionStore
			var writeLog, readLog port.CursorEventLog
			switch backend {
			case "jsonl":
				dir := t.TempDir()
				w, err := jsonlstore.New(dir)
				check(t, err)
				r, err := jsonlstore.New(dir)
				check(t, err)
				writer, reader, writeLog, readLog = w, r, w, r
			case "redis":
				broker := miniredis.RunT(t)
				w, err := redisstore.New(broker.Addr())
				check(t, err)
				r, err := redisstore.New(broker.Addr())
				check(t, err)
				t.Cleanup(func() { check(t, r.Close()); check(t, w.Close()) })
				writer, reader, writeLog, readLog = w, r, w, r
			case "grpc":
				dir := t.TempDir()
				r, err := jsonlstore.New(dir)
				check(t, err)
				lis := bufconn.Listen(1 << 20)
				server := grpc.NewServer(grpc.MaxRecvMsgSize(grpcdriver.MaxSnapshotBytes))
				driverv1.RegisterSessionStoreServiceServer(server, grpcdriver.NewSessionStoreServer(r))
				driverv1.RegisterEventLogServiceServer(server, grpcdriver.NewEventLogServer(r))
				go func() { _ = server.Serve(lis) }()
				t.Cleanup(func() { server.Stop(); check(t, lis.Close()) })
				dial := func() *grpc.ClientConn {
					t.Helper()
					conn, err := grpc.NewClient("passthrough:///consumer", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
					check(t, err)
					t.Cleanup(func() { check(t, conn.Close()) })
					return conn
				}
				wc, rc := dial(), dial()
				writer, err = grpcdriver.NewSessionStore(t.Context(), wc)
				check(t, err)
				reader, err = grpcdriver.NewSessionStore(t.Context(), rc)
				check(t, err)
				writeLog, readLog = grpcdriver.NewEventLog(wc), grpcdriver.NewEventLog(rc)
			}
			ctx := t.Context()
			ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "external", Revision: "1"}
			root := session.New("root", session.ModeDefault, ref, session.Limits{}, time.Now())
			root.SetUsageAttribution("provider", "model")
			check(t, root.BeginTurn())
			check(t, root.RecordUsage(session.Usage{InputTokens: 11, OutputTokens: 3}))
			check(t, writer.Save(ctx, root))
			second := session.New("second", session.ModeDefault, ref, session.Limits{}, time.Now().Add(-time.Minute))
			relationship := session.SessionRelationship{ParentSessionID: root.ID, ParentIncarnation: root.Incarnation(), CallID: "external-child"}
			check(t, second.RestoreSessionMetadata(session.SessionKindSubagent, relationship))
			check(t, writer.Save(ctx, second))
			child, err := reader.Load(ctx, second.ID)
			check(t, err)
			if child.ID != second.ID || child.Kind != session.SessionKindSubagent || child.Incarnation() != second.Incarnation() || child.Relationship != relationship {
				t.Fatalf("persisted child relationship/lifetime lost: %+v", child)
			}
			loaded, err := reader.Load(ctx, "root")
			check(t, err)
			if loaded.ID != root.ID || loaded.Kind != root.Kind || loaded.Relationship != root.Relationship || loaded.UsageFor(session.UsageKindMain).InputTokens != 11 || loaded.Incarnation() != root.Incarnation() {
				t.Fatalf("persisted usage/lifetime lost: %+v", loaded)
			}
			inventory, err := reader.(port.PrunableStore).List(ctx)
			check(t, err)
			if len(inventory) != 2 {
				t.Fatalf("discovered sessions: %+v", inventory)
			}
			pager := reader.(port.SessionMetadataPager)
			page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
			check(t, err)
			if len(page.Sessions) != 1 || page.NextCursor == nil || page.TotalCount != 2 {
				t.Fatalf("first page: %+v", page)
			}
			next, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, Cursor: page.NextCursor})
			check(t, err)
			if len(next.Sessions) != 1 || next.Sessions[0].ID == page.Sessions[0].ID {
				t.Fatalf("next page: %+v", next)
			}
			lineage, err := reader.(port.SessionLineageReader).ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
			check(t, err)
			if lineage.Truncated || len(lineage.Records) != 2 || lineage.Records[0].ID != root.ID || lineage.Records[0].Incarnation != string(root.Incarnation()) || lineage.Records[0].State != port.SessionLineageRetained {
				t.Fatalf("lineage: %+v", lineage)
			}
			childRecord := lineage.Records[1]
			if childRecord.ID != second.ID || childRecord.Incarnation != string(second.Incarnation()) || childRecord.Kind != session.SessionKindSubagent || childRecord.Relationship != relationship || childRecord.State != port.SessionLineageRetained {
				t.Fatalf("child lineage: %+v", childRecord)
			}
			ev := session.Event{Type: session.EvMessageDelta, Text: "first", Seq: 1}
			first, err := writeLog.AppendEvent(ctx, root.ID, ev)
			check(t, err)
			_, err = writeLog.AppendGap(ctx, root.ID, "failed append")
			check(t, err)
			read := func(id session.SessionID, after port.Cursor, opts port.ReadOptions) ([]port.LogRecord, error) {
				t.Helper()
				var records []port.LogRecord
				for record, err := range readLog.ReadAfter(ctx, id, after, opts) {
					if err != nil {
						return records, err
					}
					records = append(records, record)
				}
				return records, nil
			}
			records, err := read(root.ID, "", port.ReadOptions{Limit: 1})
			check(t, err)
			if len(records) != 1 || records[0].Kind != port.LogRecordEvent || records[0].Cursor != first {
				t.Fatalf("initial replay: %+v", records)
			}
			records, err = read(root.ID, first, port.ReadOptions{})
			check(t, err)
			if len(records) != 1 || records[0].Kind != port.LogRecordGap || records[0].GapReason != "failed append" {
				t.Fatalf("resume gap: %+v", records)
			}
			unknown, err := read("unknown", "", port.ReadOptions{})
			check(t, err)
			if len(unknown) != 0 {
				t.Fatalf("unknown log should be empty, not a proof of complete history: %+v", unknown)
			}
			for _, bad := range []struct {
				id     session.SessionID
				cursor port.Cursor
			}{{root.ID, "not a cursor"}, {"second", first}} {
				_, err = read(bad.id, bad.cursor, port.ReadOptions{})
				if !errors.Is(err, port.ErrCursorMalformed) {
					t.Fatalf("invalid/cross-session cursor: %v", err)
				}
			}
			// Early break releases the iterator; a later independent read still works.
			for range readLog.ReadAfter(ctx, root.ID, "", port.ReadOptions{Follow: true}) {
				break
			}
			followCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			observed := make(chan port.LogRecord, 1)
			done := make(chan error, 1)
			go func() {
				for rec, err := range readLog.ReadAfter(followCtx, root.ID, records[0].Cursor, port.ReadOptions{Follow: true, Limit: 1}) {
					if err != nil {
						done <- err
						return
					}
					observed <- rec
				}
				done <- nil
			}()
			tail, err := writeLog.AppendEvent(ctx, root.ID, session.Event{Type: session.EvMessageDelta, Text: "after subscribe", Seq: 2})
			check(t, err)
			select {
			case rec := <-observed:
				if rec.Event.Text != "after subscribe" {
					t.Fatalf("follow: %+v", rec)
				}
			case <-followCtx.Done():
				t.Fatal("follow did not deliver append")
			}
			select {
			case err := <-done:
				check(t, err)
			case <-followCtx.Done():
				t.Fatal("follow did not terminate")
			}
			activeCtx, stopActive := context.WithTimeout(ctx, 10*time.Second)
			defer stopActive()
			activeRecords := make(chan port.LogRecord, 1)
			activeDone := make(chan error, 1)
			go func() {
				for rec, err := range readLog.ReadAfter(activeCtx, root.ID, tail, port.ReadOptions{Follow: true}) {
					if err != nil {
						activeDone <- err
						return
					}
					select {
					case activeRecords <- rec:
					case <-activeCtx.Done():
						return
					}
				}
				activeDone <- nil
			}()
			// A concurrent append can be replayed during attachment. Keep
			// appending until a record arrives after the follower reaches live mode.
			for seq := int64(3); ; seq++ {
				_, err = writeLog.AppendEvent(ctx, root.ID, session.Event{Type: session.EvMessageDelta, Text: "active follow", Seq: seq})
				check(t, err)
				select {
				case rec := <-activeRecords:
					if rec.Event.Text != "active follow" {
						t.Fatalf("active follow: %+v", rec)
					}
					if rec.Live {
						goto live
					}
				case err := <-activeDone:
					t.Fatalf("active follow ended before live delivery: %v", err)
				case <-activeCtx.Done():
					t.Fatal("active follow did not reach live delivery")
				}
			}
		live:
			select {
			case err := <-activeDone:
				t.Fatalf("unlimited follow ended before cancellation: %v", err)
			default:
			}
			stopActive()
			select {
			case err := <-activeDone:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("active follow cancel: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("active follow did not detach on cancellation")
			}
			cancelled, stop := context.WithCancel(ctx)
			stop()
			for _, err := range readLog.ReadAfter(cancelled, root.ID, "", port.ReadOptions{Follow: true}) {
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			}
			check(t, reader.(port.PrunableStore).Delete(ctx, root.ID))
			_, err = reader.Load(ctx, root.ID)
			if !errors.Is(err, port.ErrSessionNotFound) {
				t.Fatalf("prune load: %v", err)
			}
			lineage, err = reader.(port.SessionLineageReader).ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
			check(t, err)
			if lineage.Truncated || len(lineage.Records) != 2 || lineage.Records[0].ID != root.ID || lineage.Records[0].Incarnation != string(root.Incarnation()) || lineage.Records[0].State != port.SessionLineagePruned || lineage.Records[1] != childRecord {
				t.Fatalf("tombstone: %+v", lineage)
			}
			_, err = writeLog.AppendEvent(ctx, root.ID, ev)
			check(t, err)
			_, err = read(root.ID, first, port.ReadOptions{})
			if !errors.Is(err, port.ErrCursorExpired) {
				t.Fatalf("old cursor after native delete: %v", err)
			}
		})
	}
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
