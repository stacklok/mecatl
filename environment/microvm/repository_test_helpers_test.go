package microvm

import "context"

func testArtifactSnapshot(verified VerifiedArtifacts) RepositoryArtifactSnapshot {
	return func(context.Context) (VerifiedArtifacts, func(), error) {
		return verified, func() {}, nil
	}
}
