package history

// Page is an owned sequence of complete decoded commits returned by History.Read.
// Canonical byte length (not decoded heap size) obeys Policy.MaxPageBytes;
// count obeys ReadRequest.MaxCommits.
// Empty success is possible only at EOF; errors return a zero Page. If the first
// commit cannot fit, Read returns PageTooSmallError, never an empty success.
type Page struct {
	// Commits owns DTOs and nested storage in contiguous sequence order.
	Commits []Commit
	// Next is the last returned accepted reference, or After (equal to Through) at EOF.
	Next CommitRef
}
