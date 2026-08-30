package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The sealed backup and the lost-device path (migration 0019, ADR 010).
//
// Against a real PostgreSQL, because what is being tested is SQL: an upsert
// whose conflict clause carries the counter comparison, a delete scoped by the
// owner's id in its WHERE, and two ON DELETE CASCADEs. A mock would agree with
// whatever this file claimed about all three.

func TestMlsBackupIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "backup")

	if _, err := fx.store.MlsBackupByUser(ctx, fx.alice.ID); !errors.Is(err, storage.ErrMlsBackupNotFound) {
		t.Fatalf("backup before any upload: %v, want ErrMlsBackupNotFound", err)
	}

	if err := fx.store.PutMlsBackup(ctx, fx.alice.ID, []byte("sealed-1"), 1); err != nil {
		t.Fatalf("PutMlsBackup: %v", err)
	}
	stored, err := fx.store.MlsBackupByUser(ctx, fx.alice.ID)
	if err != nil {
		t.Fatalf("MlsBackupByUser: %v", err)
	}
	if string(stored.Envelope) != "sealed-1" || stored.Counter != 1 {
		t.Errorf("stored %q at counter %d, want the bytes and the counter uploaded", stored.Envelope, stored.Counter)
	}
	if stored.UpdatedAt.IsZero() {
		t.Error("the stored backup carries no timestamp; a restore has no date to disagree with")
	}

	// One row per account, replaced in place — never a second row and never an
	// appended history the server could choose between.
	if err := fx.store.PutMlsBackup(ctx, fx.alice.ID, []byte("sealed-2"), 2); err != nil {
		t.Fatalf("replacing the backup: %v", err)
	}
	replaced, err := fx.store.MlsBackupByUser(ctx, fx.alice.ID)
	if err != nil {
		t.Fatalf("MlsBackupByUser after replacing: %v", err)
	}
	if string(replaced.Envelope) != "sealed-2" || replaced.Counter != 2 {
		t.Errorf("after replacing: %q at counter %d, want sealed-2 at 2", replaced.Envelope, replaced.Counter)
	}
	if replaced.UpdatedAt.Before(stored.UpdatedAt) {
		t.Error("the replacement kept an older timestamp than the row it replaced")
	}

	// The whole point of the counter column: a write that does not move
	// forward is refused, and — the half worth asserting — the stored envelope
	// is untouched by the refusal.
	for _, counter := range []int64{2, 1} {
		if err := fx.store.PutMlsBackup(ctx, fx.alice.ID, []byte("stale"), counter); !errors.Is(err, storage.ErrMlsBackupStale) {
			t.Errorf("uploading at counter %d: %v, want ErrMlsBackupStale", counter, err)
		}
	}
	survived, err := fx.store.MlsBackupByUser(ctx, fx.alice.ID)
	if err != nil {
		t.Fatalf("MlsBackupByUser after the refusals: %v", err)
	}
	if string(survived.Envelope) != "sealed-2" || survived.Counter != 2 {
		t.Errorf("a refused write changed the row: %q at %d", survived.Envelope, survived.Counter)
	}

	// One account's blob is not another's. The user id IS the primary key, so
	// there is no argument anywhere that could reach across.
	if _, err := fx.store.MlsBackupByUser(ctx, fx.bob.ID); !errors.Is(err, storage.ErrMlsBackupNotFound) {
		t.Errorf("bob sees alice's backup: %v, want ErrMlsBackupNotFound", err)
	}
	if err := fx.store.PutMlsBackup(ctx, fx.bob.ID, []byte("bob-1"), 1); err != nil {
		t.Fatalf("bob's own upload: %v", err)
	}
	if alices, getErr := fx.store.MlsBackupByUser(ctx, fx.alice.ID); getErr != nil || string(alices.Envelope) != "sealed-2" {
		t.Errorf("bob's upload landed on alice's row: %q (err %v)", alices.Envelope, getErr)
	}

	// Deleting is idempotent, and it is scoped: bob's row survives alice's
	// delete.
	if err := fx.store.DeleteMlsBackup(ctx, fx.alice.ID); err != nil {
		t.Fatalf("DeleteMlsBackup: %v", err)
	}
	if err := fx.store.DeleteMlsBackup(ctx, fx.alice.ID); err != nil {
		t.Errorf("deleting twice: %v, want success — the asked-for state is already true", err)
	}
	if err := fx.store.DeleteMlsBackup(ctx, uuid.New()); err != nil {
		t.Errorf("deleting for an account that does not exist: %v, want success", err)
	}
	if _, err := fx.store.MlsBackupByUser(ctx, fx.alice.ID); !errors.Is(err, storage.ErrMlsBackupNotFound) {
		t.Errorf("alice's backup survived the delete: %v", err)
	}
	if bobs, getErr := fx.store.MlsBackupByUser(ctx, fx.bob.ID); getErr != nil || string(bobs.Envelope) != "bob-1" {
		t.Errorf("alice's delete took bob's row with it: %q (err %v)", bobs.Envelope, getErr)
	}

	// After a delete the counter starts over, because there is no row to
	// compare against. That is correct and is worth pinning: the anti-rollback
	// control is the client's floor, and a server-side memory of counters past
	// a deletion the user asked for would be state nobody wants kept.
	if err := fx.store.PutMlsBackup(ctx, fx.alice.ID, []byte("fresh"), 1); err != nil {
		t.Errorf("uploading after a delete: %v, want success", err)
	}
}

// TestDeregisterMlsDeviceIntegration pins the mechanism the whole lost-device
// path rests on: after deregistration the key stops appearing in the roster
// every other member's client sweeps against (ADR 007). Nothing else in this
// slice makes a stolen leaf evictable.
func TestDeregisterMlsDeviceIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "deregister")

	laptop := mustRegisterDevice(ctx, t, fx.store, fx.alice.ID, "alice-laptop")
	mustRegisterDevice(ctx, t, fx.store, fx.alice.ID, "alice-phone")
	mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-laptop")

	if keys := rosterKeys(ctx, t, fx, fx.alice.ID); len(keys) != 2 {
		t.Fatalf("alice starts with %v in the directory, want both her devices", keys)
	}

	// Owner-scoped both ways, and both answer alike. Bob cannot drop alice's
	// laptop, and an id that names nothing is the same refusal — so a guess
	// confirms nothing about anybody's devices.
	if err := fx.store.DeregisterMlsDevice(ctx, fx.bob.ID, laptop.ID); !errors.Is(err, storage.ErrMlsDeviceNotFound) {
		t.Errorf("bob deregistering alice's device: %v, want ErrMlsDeviceNotFound", err)
	}
	if err := fx.store.DeregisterMlsDevice(ctx, fx.alice.ID, uuid.New()); !errors.Is(err, storage.ErrMlsDeviceNotFound) {
		t.Errorf("deregistering an id that names nothing: %v, want ErrMlsDeviceNotFound", err)
	}
	if keys := rosterKeys(ctx, t, fx, fx.alice.ID); len(keys) != 2 {
		t.Errorf("a refused deregistration changed the directory: %v", keys)
	}

	// The mechanism.
	if err := fx.store.DeregisterMlsDevice(ctx, fx.alice.ID, laptop.ID); err != nil {
		t.Fatalf("DeregisterMlsDevice: %v", err)
	}
	keys := rosterKeys(ctx, t, fx, fx.alice.ID)
	if len(keys) != 1 || keys[0] != "alice-phone" {
		t.Errorf("after deregistering the laptop the roster lists %v, want only the phone", keys)
	}
	// Everybody else's rows are untouched — the sweep must not evict a
	// bystander because somebody lost a laptop.
	if theirs := rosterKeys(ctx, t, fx, fx.bob.ID); len(theirs) != 1 || theirs[0] != "bob-laptop" {
		t.Errorf("bob's directory entry changed: %v", theirs)
	}

	// Already gone is the same answer as never existed, which is what makes a
	// repeated call carry no information.
	if err := fx.store.DeregisterMlsDevice(ctx, fx.alice.ID, laptop.ID); !errors.Is(err, storage.ErrMlsDeviceNotFound) {
		t.Errorf("deregistering twice: %v, want ErrMlsDeviceNotFound", err)
	}
}

// TestDeregisterMlsDeviceStrandsNothing pins the cascades: an unclaimed key
// package cannot be handed out for a device that is gone, and a Welcome
// addressed to it does not sit in a list its recipient can never acknowledge.
func TestDeregisterMlsDeviceStrandsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newMlsFixture(ctx, t, "strand")
	mustCreateGroup(ctx, t, fx.store, fx.channelID, "strand-group")

	laptop := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-laptop")
	phone := mustRegisterDevice(ctx, t, fx.store, fx.bob.ID, "bob-phone")
	if _, err := fx.store.ReplaceMlsKeyPackages(ctx, fx.bob.ID, laptop.ID, [][]byte{[]byte("kp-laptop")}); err != nil {
		t.Fatalf("publishing the laptop's pool: %v", err)
	}
	if _, err := fx.store.ReplaceMlsKeyPackages(ctx, fx.bob.ID, phone.ID, [][]byte{[]byte("kp-phone")}); err != nil {
		t.Fatalf("publishing the phone's pool: %v", err)
	}

	// One Welcome covering both of bob's devices — the shape MLS produces.
	if _, err := fx.store.SubmitMlsCommit(ctx, storage.NewMlsCommit{
		ChannelID: fx.channelID,
		Epoch:     0,
		Message:   []byte("adding bob"),
		Welcomes: []storage.MlsWelcomeDelivery{
			{DeviceIDs: []uuid.UUID{laptop.ID, phone.ID}, Welcome: []byte("for both")},
		},
	}); err != nil {
		t.Fatalf("SubmitMlsCommit: %v", err)
	}
	if welcomes := listWelcomes(ctx, t, fx, fx.bob.ID); len(welcomes) != 2 {
		t.Fatalf("bob has %d welcomes before the loss, want 2", len(welcomes))
	}

	if err := fx.store.DeregisterMlsDevice(ctx, fx.bob.ID, laptop.ID); err != nil {
		t.Fatalf("DeregisterMlsDevice: %v", err)
	}

	// The pending Welcome went with the device rather than becoming a row
	// nobody can ever acknowledge.
	welcomes := listWelcomes(ctx, t, fx, fx.bob.ID)
	if len(welcomes) != 1 || welcomes[0].DeviceID != phone.ID {
		t.Errorf("after the loss bob has %d welcomes (%+v), want only the phone's", len(welcomes), welcomes)
	}

	// And the pool went too: a claim can never hand out a package addressed to
	// a device the directory no longer lists.
	claims, missing, err := fx.store.ClaimMlsKeyPackages(ctx, fx.channelID, fx.bob.ID)
	if err != nil {
		t.Fatalf("ClaimMlsKeyPackages: %v", err)
	}
	if len(claims) != 1 || claims[0].DeviceID != phone.ID || string(claims[0].KeyPackage) != "kp-phone" {
		t.Errorf("claims = %+v, want only the phone's package", claims)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none — the lost device is not missing, it is gone", missing)
	}
}

// rosterKeys reads one member's signature keys out of the channel roster —
// exactly what a client assembles its eviction allow-list from.
func rosterKeys(ctx context.Context, t *testing.T, fx mlsFixture, userID uuid.UUID) []string {
	t.Helper()

	members, err := fx.store.ListMlsMemberDevices(ctx, fx.channelID, nil, 50)
	if err != nil {
		t.Fatalf("ListMlsMemberDevices: %v", err)
	}
	byUser := memberKeys(members)
	keys, listed := byUser[userID]
	if !listed {
		t.Fatalf("%s is not on the channel roster at all", userID)
	}
	return keys
}

func listWelcomes(ctx context.Context, t *testing.T, fx mlsFixture, userID uuid.UUID) []storage.MlsWelcome {
	t.Helper()

	welcomes, err := fx.store.ListMlsWelcomes(ctx, userID)
	if err != nil {
		t.Fatalf("ListMlsWelcomes: %v", err)
	}
	return welcomes
}
