package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The E2EE transport's storage tests (ADR 006, migration 0017).
//
// Every one of them runs against a real PostgreSQL, because what is being
// tested is not Go: it is a compare-and-swap, a consuming delete under
// SKIP LOCKED, and a transaction boundary. A mock would agree with whatever
// this file asserted and prove nothing about any of the three.

// mlsFixture is one test's world: a store, a channel, and its two members.
type mlsFixture struct {
	store     *storage.Store
	channelID uuid.UUID
	alice     storage.User
	bob       storage.User
	stranger  storage.User
}

// newMlsFixture provisions a channel with two members and one outsider.
func newMlsFixture(ctx context.Context, t *testing.T, slug string) mlsFixture {
	t.Helper()

	store, _ := testdb.New(t)
	alice := mustCreateUser(ctx, t, store, newUser(slug+"alice"))
	bob := mustCreateUser(ctx, t, store, newUser(slug+"bob"))
	stranger := mustCreateUser(ctx, t, store, newUser(slug+"stranger"))

	ch := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind:      storage.ChannelKindPrivate,
		Slug:      slug,
		E2EE:      true,
		CreatedBy: alice.ID,
	})
	if err := store.AddChannelMember(ctx, ch.ID, bob.ID, alice.ID); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	return mlsFixture{store: store, channelID: ch.ID, alice: alice, bob: bob, stranger: stranger}
}

// mustRegisterDevice registers one device and fails the test if it cannot.
func mustRegisterDevice(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID, key string) storage.MlsDevice {
	t.Helper()

	device, _, err := store.RegisterMlsDevice(ctx, userID, []byte(key))
	if err != nil {
		t.Fatalf("RegisterMlsDevice(%s): %v", key, err)
	}
	return device
}

// mustCreateGroup registers the channel's group and fails the test if it
// cannot.
func mustCreateGroup(ctx context.Context, t *testing.T, store *storage.Store, channelID uuid.UUID, groupID string) storage.MlsGroup {
	t.Helper()

	group, err := store.CreateMlsGroup(ctx, channelID, []byte(groupID))
	if err != nil {
		t.Fatalf("CreateMlsGroup: %v", err)
	}
	return group
}

func TestRegisterMlsDeviceIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "regdev")

	first, created, err := fx.store.RegisterMlsDevice(ctx, fx.alice.ID, []byte("key-one"))
	if err != nil {
		t.Fatalf("RegisterMlsDevice: %v", err)
	}
	if !created {
		t.Error("the first registration reported created=false")
	}

	// The idempotency rule: the same key under the same user is the same
	// device, so a client can call this on every startup with no bookkeeping.
	again, created, err := fx.store.RegisterMlsDevice(ctx, fx.alice.ID, []byte("key-one"))
	if err != nil {
		t.Fatalf("re-registering: %v", err)
	}
	if created {
		t.Error("re-registering the same key reported created=true")
	}
	if again.ID != first.ID {
		t.Errorf("re-registration made a second device (%s, then %s)", first.ID, again.ID)
	}

	// A different key is a different device, because a leaf is a device.
	second, created, err := fx.store.RegisterMlsDevice(ctx, fx.alice.ID, []byte("key-two"))
	if err != nil {
		t.Fatalf("second device: %v", err)
	}
	if !created || second.ID == first.ID {
		t.Errorf("a second key did not make a second device (created=%v, id=%s)", created, second.ID)
	}

	// The same key under a DIFFERENT user is a different device too: the
	// idempotency key is the pair, so one person cannot occupy another's
	// registration by guessing their key.
	theirs, created, err := fx.store.RegisterMlsDevice(ctx, fx.bob.ID, []byte("key-one"))
	if err != nil {
		t.Fatalf("bob's device: %v", err)
	}
	if !created || theirs.ID == first.ID {
		t.Errorf("bob's registration collided with alice's (created=%v, id=%s)", created, theirs.ID)
	}
}

func TestReplaceMlsKeyPackagesIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "keypkg")
	device := mustRegisterDevice(ctx, t, fx.store, fx.alice.ID, "alice-key")

	count, err := fx.store.ReplaceMlsKeyPackages(ctx, fx.alice.ID, device.ID,
		[][]byte{[]byte("kp-1"), []byte("kp-2"), []byte("kp-3")})
	if err != nil {
		t.Fatalf("ReplaceMlsKeyPackages: %v", err)
	}
	if count != 3 {
		t.Errorf("unclaimed_count = %d, want 3", count)
	}

	// Replace-all, not append: the previous pool is gone in the same
	// transaction, because the server cannot read the expiry a package
	// carries and must not guess at staleness.
	count, err = fx.store.ReplaceMlsKeyPackages(ctx, fx.alice.ID, device.ID, [][]byte{[]byte("kp-4")})
	if err != nil {
		t.Fatalf("second ReplaceMlsKeyPackages: %v", err)
	}
	if count != 1 {
		t.Errorf("unclaimed_count = %d after replacing, want 1 — the old pool survived", count)
	}

	// Own-device only. Bob publishing to alice's device is the same 404 as
	// a device that never existed, so nothing about her devices leaks.
	if _, pubErr := fx.store.ReplaceMlsKeyPackages(ctx, fx.bob.ID, device.ID, [][]byte{[]byte("stolen")}); !errors.Is(pubErr, storage.ErrMlsDeviceNotFound) {
		t.Errorf("publishing to another user's device: %v, want ErrMlsDeviceNotFound", pubErr)
	}
	if _, pubErr := fx.store.ReplaceMlsKeyPackages(ctx, fx.alice.ID, uuid.New(), [][]byte{[]byte("nowhere")}); !errors.Is(pubErr, storage.ErrMlsDeviceNotFound) {
		t.Errorf("publishing to an unknown device: %v, want ErrMlsDeviceNotFound", pubErr)
	}

	// The refused publish changed nothing.
	claims, _, err := fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.alice.ID)
	if err != nil {
		t.Fatalf("ClaimMlsKeyPackages: %v", err)
	}
	if len(claims) != 1 || string(claims[0].KeyPackage) != "kp-4" {
		t.Errorf("claims = %+v, want the one package alice published", claims)
	}
}

func TestCreateMlsGroupIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "mkgroup")

	if _, err := fx.store.MlsGroupByChannel(ctx, fx.channelID); !errors.Is(err, storage.ErrMlsGroupNotFound) {
		t.Fatalf("group before creation: %v, want ErrMlsGroupNotFound", err)
	}

	group := mustCreateGroup(ctx, t, fx.store, fx.channelID, "group-a")
	if group.Epoch != 0 {
		t.Errorf("a fresh group is at epoch %d, want 0", group.Epoch)
	}

	// One group per channel, settled by the primary key.
	if _, err := fx.store.CreateMlsGroup(ctx, fx.channelID, []byte("group-b")); !errors.Is(err, storage.ErrMlsGroupExists) {
		t.Errorf("second group on one channel: %v, want ErrMlsGroupExists", err)
	}

	// And one channel per group id, settled by the other unique index. It is
	// the same answer because it is the same fact from the caller's side.
	other := mustCreateChannel(ctx, t, fx.store, storage.NewChannel{
		Kind: storage.ChannelKindPrivate, Slug: "mkgroupother", E2EE: true, CreatedBy: fx.alice.ID,
	})
	if _, err := fx.store.CreateMlsGroup(ctx, other.ID, []byte("group-a")); !errors.Is(err, storage.ErrMlsGroupExists) {
		t.Errorf("reusing a group id: %v, want ErrMlsGroupExists", err)
	}

	if _, err := fx.store.CreateMlsGroup(ctx, uuid.New(), []byte("group-c")); !errors.Is(err, storage.ErrChannelNotFound) {
		t.Errorf("group on an unknown channel: %v, want ErrChannelNotFound", err)
	}
}

func TestClaimMlsKeyPackagesIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "claimkp")

	stocked := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-laptop")
	empty := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-phone")
	if _, err := fx.store.ReplaceMlsKeyPackages(ctx, fx.bob.ID, stocked.ID, [][]byte{[]byte("bob-kp")}); err != nil {
		t.Fatalf("stock bob's laptop: %v", err)
	}

	claims, missing, err := fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.bob.ID)
	if err != nil {
		t.Fatalf("ClaimMlsKeyPackages: %v", err)
	}
	if len(claims) != 1 || claims[0].DeviceID != stocked.ID || string(claims[0].KeyPackage) != "bob-kp" {
		t.Errorf("claims = %+v, want the laptop's one package", claims)
	}
	// A device with an empty pool is named rather than dropped: it cannot be
	// added until it replenishes, and the client says so instead of
	// pretending it added everybody.
	if len(missing) != 1 || missing[0] != empty.ID {
		t.Errorf("missing = %v, want the phone (%s)", missing, empty.ID)
	}

	// Consuming: the pool is empty now, so the same device is missing on the
	// next claim. This is the single-use rule, and it is what makes handing
	// one package to two adders impossible rather than merely forbidden.
	claims, missing, err = fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.bob.ID)
	if err != nil {
		t.Fatalf("second ClaimMlsKeyPackages: %v", err)
	}
	if len(claims) != 0 || len(missing) != 2 {
		t.Errorf("after consuming: claims=%+v missing=%v, want none claimed and both devices missing", claims, missing)
	}

	// A target with no devices at all: both lists empty, which the client
	// renders as "cannot add yet".
	claims, missing, err = fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.alice.ID)
	if err != nil {
		t.Fatalf("claiming from a member with no devices: %v", err)
	}
	if len(claims) != 0 || len(missing) != 0 {
		t.Errorf("claims=%+v missing=%v, want both empty", claims, missing)
	}

	// A target who is not a member of this channel is ErrNotFound — the
	// contract's member_not_found. The endpoint is channel-scoped precisely
	// so it cannot be walked as a public directory.
	if _, _, err := fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.stranger.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("claiming for a non-member: %v, want ErrNotFound", err)
	}
}

// TestClaimMlsKeyPackagesConcurrentIntegration is why the claim is a DELETE
// with SKIP LOCKED rather than a read followed by a delete.
//
// A key package is single-use by protocol: handing one to two adders puts the
// same leaf in two groups, which is the bug the whole endpoint exists to make
// impossible. Two claims for a device holding exactly one package must
// therefore end with one claim and one missing_device_id — never two claims,
// and never a deadlock.
func TestClaimMlsKeyPackagesConcurrentIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "claimrace")

	device := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-only")
	if _, err := fx.store.ReplaceMlsKeyPackages(ctx, fx.bob.ID, device.ID, [][]byte{[]byte("the-only-one")}); err != nil {
		t.Fatalf("stock the pool: %v", err)
	}

	const racers = 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed int
		absent  int
		failed  []error
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims, missing, err := fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.bob.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, err)
			case len(claims) == 1 && len(missing) == 0:
				claimed++
			case len(claims) == 0 && len(missing) == 1:
				absent++
			default:
				failed = append(failed, fmt.Errorf("claims=%+v missing=%v", claims, missing))
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(failed) > 0 {
		t.Fatalf("concurrent claims failed: %v", failed)
	}
	if claimed != 1 || absent != 1 {
		t.Errorf("%d claimers got the package and %d found none; want exactly 1 and 1", claimed, absent)
	}
}

func TestSubmitMlsCommitIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "commit")
	mustCreateGroup(ctx, t, fx.store, fx.channelID, "commit-group")
	device := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-device")

	// A commit built at epoch 0 advances the group to 1, and the commit row
	// carries the epoch the group REACHED — which is what makes
	// after_epoch=<my epoch> return the commit a client at that epoch needs.
	out, err := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID,
		Epoch:     0,
		Message:   []byte("commit-one"),
		Welcomes:  []storage.MlsWelcomeDelivery{{DeviceID: device.ID, Welcome: []byte("welcome-bob")}},
	})
	if err != nil {
		t.Fatalf("SubmitMlsCommit: %v", err)
	}
	if out.Epoch != 1 {
		t.Errorf("new epoch = %d, want 1", out.Epoch)
	}
	if len(out.WelcomeUserIDs) != 1 || out.WelcomeUserIDs[0] != fx.bob.ID {
		t.Errorf("welcome recipients = %v, want just bob (%s)", out.WelcomeUserIDs, fx.bob.ID)
	}

	group, err := fx.store.MlsGroupByChannel(ctx, fx.channelID)
	if err != nil {
		t.Fatalf("MlsGroupByChannel: %v", err)
	}
	if group.Epoch != 1 {
		t.Errorf("group epoch = %d, want 1", group.Epoch)
	}

	// The stale epoch is refused: this is the compare-and-swap, and a client
	// that resubmits at 0 must be told to refetch rather than silently
	// overwrite the commit that won.
	if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID, Epoch: 0, Message: []byte("stale"),
	}); !errors.Is(cErr, storage.ErrMlsEpochConflict) {
		t.Errorf("resubmitting at a spent epoch: %v, want ErrMlsEpochConflict", cErr)
	}
	// A future epoch is refused by the same predicate, so a client cannot
	// skip the log by claiming to be ahead of it.
	if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID, Epoch: 99, Message: []byte("from the future"),
	}); !errors.Is(cErr, storage.ErrMlsEpochConflict) {
		t.Errorf("committing at a future epoch: %v, want ErrMlsEpochConflict", cErr)
	}

	// A channel with no group is a different answer from a stale epoch,
	// because it asks a different thing of a client: create the group.
	bare := mustCreateChannel(ctx, t, fx.store, storage.NewChannel{
		Kind: storage.ChannelKindPrivate, Slug: "commitbare", E2EE: true, CreatedBy: fx.alice.ID,
	})
	if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: bare.ID, Epoch: 0, Message: []byte("no group here"),
	}); !errors.Is(cErr, storage.ErrMlsGroupNotFound) {
		t.Errorf("committing to a channel with no group: %v, want ErrMlsGroupNotFound", cErr)
	}

	// The log reads back ascending, and the Welcome is waiting for bob.
	commits, err := fx.store.ListMlsCommits(ctx, fx.channelID, 0, 50)
	if err != nil {
		t.Fatalf("ListMlsCommits: %v", err)
	}
	if len(commits) != 1 || commits[0].Epoch != 1 || string(commits[0].Message) != "commit-one" {
		t.Fatalf("commits = %+v, want one commit at epoch 1", commits)
	}
	// Exactly one commit row, and no second one from the two refusals above.
	if caught, listErr := fx.store.ListMlsCommits(ctx, fx.channelID, 1, 50); listErr != nil || len(caught) != 0 {
		t.Errorf("commits after epoch 1 = %+v (err %v), want an empty page", caught, listErr)
	}
}

// TestSubmitMlsCommitConcurrentIntegration is the compare-and-swap under
// contention — the sequencing point of ADR 006.
//
// MLS requires that exactly one commit win each epoch. Two clients committing
// at the same epoch must therefore end with one acceptance, one 409, one
// commit row, and an epoch that moved by exactly one. Anything else forks the
// group.
func TestSubmitMlsCommitConcurrentIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "commitrace")
	mustCreateGroup(ctx, t, fx.store, fx.channelID, "race-group")

	const racers = 2
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		accepted  int
		conflicts int
		failed    []error
	)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
				ChannelID: fx.channelID,
				Epoch:     0,
				Message:   []byte(fmt.Sprintf("racer-%d", i)),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, storage.ErrMlsEpochConflict):
				conflicts++
			default:
				failed = append(failed, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(failed) > 0 {
		t.Fatalf("concurrent commits failed: %v", failed)
	}
	if accepted != 1 || conflicts != 1 {
		t.Errorf("%d commits accepted and %d conflicted; want exactly 1 and 1", accepted, conflicts)
	}

	group, err := fx.store.MlsGroupByChannel(ctx, fx.channelID)
	if err != nil {
		t.Fatalf("MlsGroupByChannel: %v", err)
	}
	if group.Epoch != 1 {
		t.Errorf("group epoch = %d after the race, want exactly 1", group.Epoch)
	}
	commits, err := fx.store.ListMlsCommits(ctx, fx.channelID, 0, 50)
	if err != nil {
		t.Fatalf("ListMlsCommits: %v", err)
	}
	if len(commits) != 1 {
		t.Errorf("the log holds %d commits after the race, want exactly 1", len(commits))
	}
}

// TestSubmitMlsCommitWelcomeAtomicityIntegration pins the transaction
// boundary: a Welcome that cannot be stored takes the whole commit with it.
//
// A committed add whose Welcome was lost is a forked group — the old members
// believe they added somebody who can never join. So a failing Welcome must
// leave no commit row and an epoch that did not move, not a commit the group
// is stuck one epoch past.
func TestSubmitMlsCommitWelcomeAtomicityIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "welcomeatomic")
	mustCreateGroup(ctx, t, fx.store, fx.channelID, "atomic-group")
	good := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-good")

	// The second Welcome names a device that does not exist. The insert is
	// one statement over both, so this is the foreign key firing inside the
	// commit's transaction.
	_, err := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID,
		Epoch:     0,
		Message:   []byte("commit-with-a-bad-welcome"),
		Welcomes: []storage.MlsWelcomeDelivery{
			{DeviceID: good.ID, Welcome: []byte("this one is fine")},
			{DeviceID: uuid.New(), Welcome: []byte("this device does not exist")},
		},
	})
	if !errors.Is(err, storage.ErrMlsDeviceNotFound) {
		t.Fatalf("commit with an unknown welcome recipient: %v, want ErrMlsDeviceNotFound", err)
	}

	group, err := fx.store.MlsGroupByChannel(ctx, fx.channelID)
	if err != nil {
		t.Fatalf("MlsGroupByChannel: %v", err)
	}
	if group.Epoch != 0 {
		t.Errorf("the failed commit advanced the epoch to %d; want it still 0", group.Epoch)
	}
	commits, err := fx.store.ListMlsCommits(ctx, fx.channelID, 0, 50)
	if err != nil {
		t.Fatalf("ListMlsCommits: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("the failed commit left %d rows in the log; want none", len(commits))
	}
	// And the Welcome that WOULD have worked is not there either: it shared
	// the transaction, so it rolled back with everything else.
	welcomes, err := fx.store.ListMlsWelcomes(ctx, fx.bob.ID)
	if err != nil {
		t.Fatalf("ListMlsWelcomes: %v", err)
	}
	if len(welcomes) != 0 {
		t.Errorf("the failed commit left %d welcomes behind; want none", len(welcomes))
	}

	// The epoch is genuinely still available: the group was not left in a
	// state where no commit can ever be accepted.
	if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID, Epoch: 0, Message: []byte("the retry"),
	}); cErr != nil {
		t.Fatalf("retrying at epoch 0 after the rollback: %v", cErr)
	}
}

func TestListMlsCommitsIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "commitlog")
	mustCreateGroup(ctx, t, fx.store, fx.channelID, "log-group")

	for epoch := range int64(5) {
		if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
			ChannelID: fx.channelID, Epoch: epoch, Message: []byte(fmt.Sprintf("c%d", epoch)),
		}); cErr != nil {
			t.Fatalf("commit at epoch %d: %v", epoch, cErr)
		}
	}

	all, err := fx.store.ListMlsCommits(ctx, fx.channelID, 0, 50)
	if err != nil {
		t.Fatalf("ListMlsCommits: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("log holds %d commits, want 5", len(all))
	}
	for i, c := range all {
		if c.Epoch != int64(i+1) {
			t.Fatalf("commit %d is at epoch %d; the log must ascend from 1", i, c.Epoch)
		}
	}

	// Epochs are the cursor: the last epoch received is the next
	// after_epoch, and an empty page means caught up.
	page, err := fx.store.ListMlsCommits(ctx, fx.channelID, 2, 2)
	if err != nil {
		t.Fatalf("paged ListMlsCommits: %v", err)
	}
	if len(page) != 2 || page[0].Epoch != 3 || page[1].Epoch != 4 {
		t.Errorf("page after epoch 2 = %+v, want epochs 3 and 4", page)
	}
	caught, err := fx.store.ListMlsCommits(ctx, fx.channelID, 5, 50)
	if err != nil {
		t.Fatalf("caught-up ListMlsCommits: %v", err)
	}
	if len(caught) != 0 {
		t.Errorf("a caught-up client got %d commits, want none", len(caught))
	}
}

func TestMlsWelcomesIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "welcomes")
	group := mustCreateGroup(ctx, t, fx.store, fx.channelID, "welcome-group")
	laptop := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-laptop")
	phone := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-phone")

	if _, cErr := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID,
		Epoch:     0,
		Message:   []byte("adding bob"),
		Welcomes: []storage.MlsWelcomeDelivery{
			{DeviceID: laptop.ID, Welcome: []byte("for the laptop")},
			{DeviceID: phone.ID, Welcome: []byte("for the phone")},
		},
	}); cErr != nil {
		t.Fatalf("SubmitMlsCommit: %v", cErr)
	}

	// All of a user's devices on one list: a Welcome is encrypted to one
	// device's key package, so a sibling holds bytes it cannot open.
	welcomes, err := fx.store.ListMlsWelcomes(ctx, fx.bob.ID)
	if err != nil {
		t.Fatalf("ListMlsWelcomes: %v", err)
	}
	if len(welcomes) != 2 {
		t.Fatalf("bob has %d welcomes, want 2", len(welcomes))
	}
	for _, wl := range welcomes {
		if wl.ChannelID != fx.channelID || string(wl.GroupID) != string(group.GroupID) {
			t.Errorf("welcome %+v does not name the channel's group", wl)
		}
	}

	// Nobody else's list is touched by them.
	if others, listErr := fx.store.ListMlsWelcomes(ctx, fx.alice.ID); listErr != nil || len(others) != 0 {
		t.Errorf("alice sees %d of bob's welcomes (err %v); want none", len(others), listErr)
	}

	// Another user's welcome answers not-found and, crucially, is NOT
	// deleted by the attempt.
	if ackErr := fx.store.DeleteMlsWelcome(ctx, fx.alice.ID, welcomes[0].ID); !errors.Is(ackErr, storage.ErrMlsWelcomeNotFound) {
		t.Errorf("acknowledging another user's welcome: %v, want ErrMlsWelcomeNotFound", ackErr)
	}
	if still, listErr := fx.store.ListMlsWelcomes(ctx, fx.bob.ID); listErr != nil || len(still) != 2 {
		t.Errorf("the refused acknowledgement removed a welcome: %d left (err %v)", len(still), listErr)
	}

	// The owner's acknowledgement removes exactly one, and repeating it is
	// success — a client that acknowledges twice asked for a state that is
	// already true.
	if ackErr := fx.store.DeleteMlsWelcome(ctx, fx.bob.ID, welcomes[0].ID); ackErr != nil {
		t.Fatalf("DeleteMlsWelcome: %v", ackErr)
	}
	if ackErr := fx.store.DeleteMlsWelcome(ctx, fx.bob.ID, welcomes[0].ID); ackErr != nil {
		t.Errorf("acknowledging twice: %v, want success (it is idempotent)", ackErr)
	}
	if ackErr := fx.store.DeleteMlsWelcome(ctx, fx.bob.ID, uuid.New()); ackErr != nil {
		t.Errorf("acknowledging an id that names nothing: %v, want success", ackErr)
	}
	left, err := fx.store.ListMlsWelcomes(ctx, fx.bob.ID)
	if err != nil {
		t.Fatalf("ListMlsWelcomes: %v", err)
	}
	if len(left) != 1 || left[0].ID != welcomes[1].ID {
		t.Errorf("welcomes left = %+v, want only the second one", left)
	}
}
