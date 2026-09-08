package tool

// DispatchSerial is an OPTIONAL marker for a read-only Tool whose sibling call
// must form a run-local dispatch barrier. The dispatcher flushes the preceding
// read batch, runs the marked call alone, then starts a following read batch.
// This changes neither ReadOnly semantics nor advertisement.
//
// The guarantee applies only among sibling calls in one dispatch within one
// run. Concurrent runs are independent; a shared adapter or other stateful
// boundary used by multiple runs must provide its own synchronization.
type DispatchSerial interface {
	Tool
	DispatchSerialTool()
}
