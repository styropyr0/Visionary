package analytics

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// replicaPair is two servers on one database, as two replicas would be. Set
// VISIONARY_TEST_DATABASE_URL to run on a scratch PostgreSQL or MySQL.
func replicaPair(t *testing.T) (a, b *Server) {
	t.Helper()
	dsn := os.Getenv("VISIONARY_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "sqlite:///" + filepath.ToSlash(filepath.Join(t.TempDir(), "leases.db"))
	}
	open := func(id string) *Server {
		s, err := New(Config{DatabaseURL: dsn}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		if err != nil {
			t.Fatal(err)
		}
		replicaStates.Store(s, &replicaState{id: id, started: nowTS()})
		t.Cleanup(func() { _ = s.db.Close() })
		return s
	}
	a = open("a")
	if os.Getenv("VISIONARY_TEST_DATABASE_URL") != "" {
		emptyTestDatabase(t, a)
	}
	if err := a.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, open("b")
}

func holderOf(t *testing.T, s *Server, name string) string {
	t.Helper()
	rows, err := s.readLeases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows[name].holder
}

func TestLeaseAcquireRenewStealRelease(t *testing.T) {
	a, b := replicaPair(t)
	ctx := context.Background()
	if err := a.tryLease(ctx, "job", false); err != nil {
		t.Fatal(err)
	}
	if err := b.tryLease(ctx, "job", true); err != nil {
		t.Fatal(err)
	}
	if h := holderOf(t, a, "job"); h != "a" {
		t.Fatalf("holder %q, want a: a held lease must not be taken", h)
	}

	if err := a.renewLeases(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := a.readLeases(ctx)
	if left := rows["job"].expiresMs - rows["job"].acquired; left < leaseTTL.Milliseconds()-1000 {
		t.Fatalf("renewed lease ends %d ms after it was taken", left)
	}

	// a stops renewing; once the lease has expired b takes it.
	if err := a.db.Exec(ctx, `UPDATE visionary_leases SET expires_ms = expires_ms - 60000`); err != nil {
		t.Fatal(err)
	}
	if err := b.tryLease(ctx, "job", true); err != nil {
		t.Fatal(err)
	}
	if h := holderOf(t, a, "job"); h != "b" {
		t.Fatalf("holder %q, want b after expiry", h)
	}
	// a renewing late doesn't take it back.
	if err := a.renewLeases(ctx); err != nil {
		t.Fatal(err)
	}
	if h := holderOf(t, a, "job"); h != "b" {
		t.Fatalf("holder %q after a's late renewal, want b", h)
	}
	if err := a.releaseLease(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if h := holderOf(t, a, "job"); h != "b" {
		t.Fatalf("a released b's lease")
	}
	if err := b.releaseLease(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if h := holderOf(t, a, "job"); h != "" {
		t.Fatalf("holder %q after release", h)
	}
}

type countingJob struct{ running, starts atomic.Int32 }

func (j *countingJob) run(ctx context.Context) {
	j.starts.Add(1)
	j.running.Add(1)
	<-ctx.Done()
	j.running.Add(-1)
}

func TestLeaseJobRunsOnceAndHandsOver(t *testing.T) {
	a, b := replicaPair(t)
	ja, jb := &countingJob{}, &countingJob{}
	a.leased("job", ja.run)
	b.leased("job", jb.run)
	actx, stopA := context.WithCancel(context.Background())
	defer stopA()
	bctx, stopB := context.WithCancel(context.Background())
	defer stopB()

	a.leaseRound(actx)
	b.leaseRound(bctx)
	waitFor(t, func() bool { return ja.running.Load() == 1 })
	a.leaseRound(actx)
	b.leaseRound(bctx)
	if ja.starts.Load() != 1 || jb.starts.Load() != 0 {
		t.Fatalf("starts a=%d b=%d, want the job on a only", ja.starts.Load(), jb.starts.Load())
	}

	// a dies without a word: its lease expires and b takes over.
	if err := a.db.Exec(context.Background(), `UPDATE visionary_leases SET expires_ms = 0`); err != nil {
		t.Fatal(err)
	}
	b.leaseRound(bctx)
	waitFor(t, func() bool { return jb.running.Load() == 1 })
	// If a was only slow, it learns at its next round and stops.
	a.leaseRound(actx)
	waitFor(t, func() bool { return ja.running.Load() == 0 })

	// b shuts down cleanly: a takes over at its next round, without waiting
	// for the lease to expire.
	stopB()
	b.leasesStop()
	if h := holderOf(t, a, "job"); h != "" {
		t.Fatalf("holder %q after shutdown, want none", h)
	}
	a.leaseRound(actx)
	waitFor(t, func() bool { return ja.running.Load() == 1 })
	if jb.running.Load() != 0 || ja.starts.Load() != 2 {
		t.Fatalf("running b=%d, starts a=%d", jb.running.Load(), ja.starts.Load())
	}
}

func TestLeasesSpreadAcrossReplicas(t *testing.T) {
	a, b := replicaPair(t)
	names := []string{"j1", "j2", "j3", "j4"}
	jobs := map[*Server][]*countingJob{}
	for _, s := range []*Server{a, b} {
		for _, n := range names {
			j := &countingJob{}
			jobs[s] = append(jobs[s], j)
			s.leased(n, j.run)
		}
	}
	running := func(s *Server) int {
		n := 0
		for _, j := range jobs[s] {
			n += int(j.running.Load())
		}
		return n
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.leaseRound(ctx)
	waitFor(t, func() bool { return running(a) == 4 })
	for range 10 {
		b.leaseRound(ctx)
		a.leases().mu.Lock()
		a.leases().shedAt = time.Time{}
		a.leases().mu.Unlock()
		a.leaseRound(ctx)
		time.Sleep(50 * time.Millisecond)
		// A lease a just gave up is free for b at once; a waits its turn.
		b.leaseRound(ctx)
		if running(a) == 2 && running(b) == 2 {
			break
		}
	}
	waitFor(t, func() bool { return running(a) == 2 && running(b) == 2 })
	for i := range names {
		if jobs[a][i].running.Load()+jobs[b][i].running.Load() != 1 {
			t.Fatalf("%s runs %d times", names[i], jobs[a][i].running.Load()+jobs[b][i].running.Load())
		}
	}
}

func TestClaimOnceAcrossReplicas(t *testing.T) {
	a, b := replicaPair(t)
	ctx := context.Background()
	if ok, err := a.claimOnce(ctx, "saml:r1", time.Minute); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := b.claimOnce(ctx, "saml:r1", time.Minute); err != nil || ok {
		t.Fatalf("replayed claim on the other replica: %v %v", ok, err)
	}
	if ok, _ := b.claimOnce(ctx, "saml:r2", time.Minute); !ok {
		t.Fatal("a new key was refused")
	}
}

func TestSignOutReachesOtherReplicas(t *testing.T) {
	a, b := replicaPair(t)
	sess := &session{UserID: "u", Token: "tok", Expires: time.Now().Add(time.Hour).Unix()}
	a.shareSignOut(sess)
	waitFor(t, func() bool {
		b.pullSignOuts(context.Background())
		b.auth.mu.Lock()
		defer b.auth.mu.Unlock()
		_, ok := b.auth.signedOut[tokenKey("tok")]
		return ok
	})
}
