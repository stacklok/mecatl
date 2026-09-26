package sessiondebug

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	continuationPrefix          = "dbgcur.v1."
	maxContinuationLen          = 16 << 10
	continuationInvalid         = "continuation is invalid or stale; restart this view without cursors"
	continuationUnsupported     = "configured event log does not support resumable reads"
	continuationUnrepresentable = "continuation could not be represented within the response bound"
)

type continuationClaim struct {
	Root       string      `json:"root"`
	Owner      string      `json:"owner"`
	Scope      string      `json:"scope"`
	View       string      `json:"view"`
	Purpose    string      `json:"purpose"`
	Start      port.Cursor `json:"start,omitempty"`
	BeforeEnd  port.Cursor `json:"before_end,omitempty"`
	End        port.Cursor `json:"end,omitempty"`
	Next       port.Cursor `json:"next,omitempty"`
	ID         string      `json:"id"`
	Records    int         `json:"records"`
	Events     int         `json:"events"`
	Gaps       int         `json:"gaps"`
	Complete   bool        `json:"complete"`
	HasMore    bool        `json:"has_more"`
	StopReason string      `json:"stop"`
	Row        int         `json:"row,omitempty"`
	Basis      string      `json:"basis,omitempty"`
}

type positionedEvent struct {
	Event  session.Event
	Before port.Cursor
	Cursor port.Cursor
}

type eventScan struct {
	Events                []session.Event
	Positioned            []positionedEvent
	Start                 port.Cursor
	BeforeEnd             port.Cursor
	End                   port.Cursor
	Next                  port.Cursor
	ID                    string
	Records               int
	EventCount            int
	GapCount              *int
	Complete              bool
	HasMore               bool
	StopReason            string
	ContinuationSupported bool
	ContinuationError     string
	RowOffset             int
	Basis                 string
}

type eventWindowEvidence struct {
	ID                    string `json:"id,omitempty"`
	ContinuationSupported bool   `json:"continuation_supported"`
	RecordLimit           int    `json:"record_limit"`
	RecordsScanned        int    `json:"records_scanned"`
	ScannedEvents         int    `json:"scanned_events"`
	GapRecords            *int   `json:"gap_records"`
	ScanComplete          bool   `json:"scan_complete"`
	RetentionComplete     bool   `json:"retention_complete"`
	StopReason            string `json:"stop_reason"`
	HasMoreRecords        bool   `json:"has_more_records"`
}

type rowPageEvidence struct {
	Limit              int  `json:"limit"`
	Returned           int  `json:"returned"`
	ProjectionComplete bool `json:"projection_complete"`
	HasMoreRows        bool `json:"has_more_rows"`
}

func (s eventScan) evidence() eventWindowEvidence {
	return eventWindowEvidence{
		ID: s.ID, ContinuationSupported: s.ContinuationSupported,
		RecordLimit: maxPerformanceScan, RecordsScanned: s.Records,
		ScannedEvents: s.EventCount, GapRecords: s.GapCount,
		ScanComplete: s.Complete, RetentionComplete: false,
		StopReason: s.StopReason, HasMoreRecords: s.HasMore,
	}
}

func (t *inspectTool) continuationCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("mecatl.inspect-session.continuation-key/v1\x00" + t.expectedFingerprint + "\x00" + base64.RawURLEncoding.EncodeToString(t.expectedOwnerScope[:])))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (t *inspectTool) sealContinuation(claim continuationClaim) (string, error) {
	plain, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	aead, err := t.continuationCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plain, []byte(continuationPrefix))
	token := continuationPrefix + base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(token) > maxContinuationLen {
		return "", errors.New(continuationUnrepresentable)
	}
	return token, nil
}

func (t *inspectTool) openClaim(token, view, scope string) (continuationClaim, error) {
	if len(token) > maxContinuationLen || !strings.HasPrefix(token, continuationPrefix) {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, continuationPrefix))
	if err != nil {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	aead, err := t.continuationCipher()
	if err != nil || len(raw) < aead.NonceSize() {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(continuationPrefix))
	if err != nil {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	var claim continuationClaim
	if json.Unmarshal(plain, &claim) != nil || claim.Root != t.expectedFingerprint || claim.Owner != base64.RawURLEncoding.EncodeToString(t.expectedOwnerScope[:]) || claim.Scope != scope || claim.View != view || claim.ID == "" {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	return claim, nil
}

func (t *inspectTool) openContinuation(token, view, scope string) (continuationClaim, error) {
	claim, err := t.openClaim(token, view, scope)
	if err != nil || (claim.Purpose != "row" && claim.Purpose != "window") {
		return continuationClaim{}, errors.New(continuationInvalid)
	}
	return claim, nil
}

func (t *inspectTool) claimFor(scan eventScan, view, scope, purpose string, row int) continuationClaim {
	return continuationClaim{
		Root: t.expectedFingerprint, Owner: base64.RawURLEncoding.EncodeToString(t.expectedOwnerScope[:]), Scope: scope,
		View: view, Purpose: purpose, Start: scan.Start, BeforeEnd: scan.BeforeEnd, End: scan.End, Next: scan.Next,
		ID: scan.ID, Records: scan.Records, Events: scan.EventCount, Complete: scan.Complete,
		HasMore: scan.HasMore, StopReason: scan.StopReason, Row: row, Basis: scan.Basis,
		Gaps: func() int {
			if scan.GapCount == nil {
				return 0
			}
			return *scan.GapCount
		}(),
	}
}

func scanID(t *inspectTool, id session.SessionID, view, scope string, start, end port.Cursor, records int) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "mecatl.inspect-session.window/v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", t.expectedFingerprint, id, view, scope, start, end, records)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}

func (t *inspectTool) scanEvents(ctx context.Context, id session.SessionID, view, scope, token string) (eventScan, error) {
	if t.log == nil {
		if token != "" {
			return eventScan{}, errors.New(continuationInvalid)
		}
		return eventScan{GapCount: nil, StopReason: stopNotConfigured}, nil
	}
	cursorLog, cursorCapable := t.log.(port.CursorEventLog)
	if token != "" && !cursorCapable {
		return eventScan{}, errors.New(continuationInvalid)
	}
	if !cursorCapable {
		return t.scanLegacy(ctx, id)
	}

	var claim continuationClaim
	var err error
	if token != "" {
		claim, err = t.openContinuation(token, view, scope)
		if err != nil {
			return eventScan{}, err
		}
		if claim.Purpose == "row" {
			return replaySealed(ctx, cursorLog, id, claim)
		}
	}
	start := port.Cursor("")
	if claim.Purpose == "window" {
		start = claim.Next
	}
	scan, err := t.readCursorWindow(ctx, cursorLog, id, start)
	if err != nil {
		if token == "" && errors.Is(err, port.ErrCursorUnsupported) {
			return t.scanLegacy(ctx, id)
		}
		return eventScan{}, err
	}
	return scan, nil
}

func (t *inspectTool) readCursorWindow(ctx context.Context, log port.CursorEventLog, id session.SessionID, start port.Cursor) (eventScan, error) {
	gapCount := 0
	scan := eventScan{Start: start, GapCount: &gapCount, ContinuationSupported: true, StopReason: "end_of_log", Complete: true}
	previous := start
	for rec, err := range log.ReadAfter(ctx, id, start, port.ReadOptions{Limit: maxPerformanceScan + 1, Follow: false}) {
		if err != nil {
			return eventScan{}, err
		}
		if ctx.Err() != nil {
			return eventScan{}, ctx.Err()
		}
		if scan.Records == maxPerformanceScan {
			scan.HasMore, scan.Complete, scan.StopReason, scan.Next = true, false, "scan_limit", scan.End
			break
		}
		scan.BeforeEnd, scan.End = previous, rec.Cursor
		previous = rec.Cursor
		scan.Records++
		switch rec.Kind {
		case port.LogRecordEvent:
			scan.Events = append(scan.Events, rec.Event)
			scan.Positioned = append(scan.Positioned, positionedEvent{Event: rec.Event, Before: scan.BeforeEnd, Cursor: rec.Cursor})
			scan.EventCount++
		case port.LogRecordGap:
			gapCount++
		default:
			return eventScan{}, errors.New(errLogReadFailed)
		}
	}
	if ctx.Err() != nil {
		return eventScan{}, ctx.Err()
	}
	if scan.Records == 0 {
		return scan, nil
	}
	scan.ID = scanID(t, id, "", "", scan.Start, scan.End, scan.Records)
	if err := validateEndpoint(ctx, log, id, scan.BeforeEnd, scan.End); err != nil {
		return eventScan{}, err
	}
	return scan, nil
}

func replaySealed(ctx context.Context, log port.CursorEventLog, id session.SessionID, claim continuationClaim) (eventScan, error) {
	gapCount := 0
	scan := eventScan{Start: claim.Start, BeforeEnd: claim.BeforeEnd, End: claim.End, Next: claim.Next, ID: claim.ID, Records: claim.Records, EventCount: claim.Events, GapCount: &gapCount, Complete: claim.Complete, HasMore: claim.HasMore, StopReason: claim.StopReason, ContinuationSupported: true, RowOffset: claim.Row, Basis: claim.Basis}
	var last, previous port.Cursor
	previous = claim.Start
	count, events := 0, 0
	for rec, err := range log.ReadAfter(ctx, id, claim.Start, port.ReadOptions{Limit: claim.Records, Follow: false}) {
		if err != nil {
			return eventScan{}, errors.New(continuationInvalid)
		}
		if ctx.Err() != nil {
			return eventScan{}, ctx.Err()
		}
		count++
		last = rec.Cursor
		switch rec.Kind {
		case port.LogRecordGap:
			gapCount++
		case port.LogRecordEvent:
			events++
			scan.Events = append(scan.Events, rec.Event)
			scan.Positioned = append(scan.Positioned, positionedEvent{Event: rec.Event, Before: previous, Cursor: rec.Cursor})
		default:
			return eventScan{}, errors.New(continuationInvalid)
		}
		previous = rec.Cursor
	}
	if count != claim.Records || last != claim.End || events != claim.Events || gapCount != claim.Gaps {
		return eventScan{}, errors.New(continuationInvalid)
	}
	if err := validateEndpoint(ctx, log, id, claim.BeforeEnd, claim.End); err != nil {
		return eventScan{}, errors.New(continuationInvalid)
	}
	return scan, nil
}

func validateEndpoint(ctx context.Context, log port.CursorEventLog, id session.SessionID, before, expected port.Cursor) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	count := 0
	for rec, err := range log.ReadAfter(ctx, id, before, port.ReadOptions{Limit: 1, Follow: false}) {
		if err != nil || rec.Cursor != expected {
			return errors.New(continuationInvalid)
		}
		count++
	}
	if count != 1 || ctx.Err() != nil {
		return errors.New(continuationInvalid)
	}
	return nil
}

func (t *inspectTool) scanLegacy(ctx context.Context, id session.SessionID) (eventScan, error) {
	scan := eventScan{GapCount: nil, ContinuationSupported: false, ContinuationError: continuationUnsupported, Complete: true, StopReason: "end_of_log"}
	for ev, err := range t.log.Read(ctx, id) {
		if err != nil {
			return eventScan{}, err
		}
		if ctx.Err() != nil {
			return eventScan{}, ctx.Err()
		}
		if scan.Records == maxPerformanceScan {
			scan.HasMore, scan.Complete, scan.StopReason = true, false, "scan_limit"
			break
		}
		scan.Events = append(scan.Events, ev)
		scan.Records++
		scan.EventCount++
	}
	return scan, nil
}
