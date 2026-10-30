package server

import (
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func currentTestTrajectory(id session.SessionID, workspace string, stop session.StopReason, usage session.Usage, messages []session.Message) learning.Trajectory {
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory(messages); err != nil {
		panic(err)
	}
	return learning.NewSessionTrajectory(sess, workspace, stop, usage)
}
