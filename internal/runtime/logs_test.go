package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

type failingStreamTarget struct {
	*fakeTarget
	streamCalls int
}

func (f *failingStreamTarget) StreamExec(context.Context, string, ...string) (io.ReadCloser, error) {
	f.streamCalls++
	return nil, errors.New("stream unavailable")
}

func TestDockerLogsFallsBackWhenStreamExecFails(t *testing.T) {
	tgt := &failingStreamTarget{fakeTarget: newFakeTarget()}
	rt := NewDockerRuntime(tgt, "/deployments")
	r, err := rt.Logs(context.Background(), "n0", LogOpts{Follow: true, Tail: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(tgt.cmds) != 1 {
		t.Fatalf("commands=%v, want original Exec fallback", tgt.cmds)
	}
	if tgt.cmds[0][0] != "docker" || tgt.cmds[0][1] != "exec" {
		t.Fatalf("commands=%v", tgt.cmds)
	}
	if tgt.streamCalls != 1 {
		t.Fatalf("stream calls=%d", tgt.streamCalls)
	}
}

var _ target.Target = (*failingStreamTarget)(nil)
