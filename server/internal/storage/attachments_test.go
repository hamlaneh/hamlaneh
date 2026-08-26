package storage_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// attachmentWorld is one test's fixture: a store, a channel, and two users
// who can upload into it.
type attachmentWorld struct {
	store  *storage.Store
	conn   *pgx.Conn
	ctx    context.Context
	people struct {
		uploader storage.User
		other    storage.User
	}
	channelID uuid.UUID
	// otherChannelID is a second channel, for the "another channel's file"
	// half of the claim rules.
	otherChannelID uuid.UUID
}

func newAttachmentWorld(t *testing.T) attachmentWorld {
	t.Helper()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	w := attachmentWorld{store: store, conn: conn, ctx: ctx}
	w.people.uploader = mustCreateUser(ctx, t, store, newUser("uploader"))
	w.people.other = mustCreateUser(ctx, t, store, newUser("otherperson"))
	w.channelID = seedMessagesChannel(ctx, t, conn, "files")
	w.otherChannelID = seedMessagesChannel(ctx, t, conn, "elsewhere")
	return w
}

// upload records one attachment, defaulting the fields a test does not care
// about.
func (w attachmentWorld) upload(t *testing.T, channelID, uploaderID uuid.UUID, filename string) storage.Attachment {
	t.Helper()

	att, err := w.store.CreateAttachment(w.ctx, storage.NewAttachment{
		ID:          uuid.New(),
		ChannelID:   channelID,
		UploaderID:  uploaderID,
		Filename:    filename,
		ContentType: "application/octet-stream",
		SizeBytes:   11,
	})
	if err != nil {
		t.Fatalf("CreateAttachment(%s): %v", filename, err)
	}
	return att
}

// send is CreateMessage with this world's channel and uploader.
func (w attachmentWorld) send(content string, ids ...uuid.UUID) (storage.Message, bool, error) {
	return w.store.CreateMessage(w.ctx, storage.NewMessage{
		ChannelID:     w.channelID,
		AuthorID:      w.people.uploader.ID,
		ClientMsgID:   uuid.New(),
		Content:       content,
		AttachmentIDs: ids,
	})
}

// messageIDOf reads an attachment's message_id straight from SQL, so the
// claim assertions are about the table rather than about what the call
// reported back.
func (w attachmentWorld) messageIDOf(t *testing.T, id uuid.UUID) *uuid.UUID {
	t.Helper()

	var messageID *uuid.UUID
	err := w.conn.QueryRow(w.ctx, `SELECT message_id FROM attachments WHERE id = $1`, id).Scan(&messageID)
	if err != nil {
		t.Fatalf("read message_id of %s: %v", id, err)
	}
	return messageID
}

func TestCreateAttachmentStoresItsMetadataIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	width, height := 1600, 900

	att, err := w.store.CreateAttachment(w.ctx, storage.NewAttachment{
		ID:           uuid.New(),
		ChannelID:    w.channelID,
		UploaderID:   w.people.uploader.ID,
		Filename:     "عکس.png",
		ContentType:  "image/png",
		SizeBytes:    4096,
		Width:        &width,
		Height:       &height,
		HasThumbnail: true,
	})
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if att.MessageID != nil {
		t.Errorf("a fresh upload is attached to message %s; it must be unattached", *att.MessageID)
	}
	if att.Filename != "عکس.png" || att.ContentType != "image/png" || att.SizeBytes != 4096 {
		t.Errorf("stored metadata came back as %+v", att)
	}
	if att.Width == nil || *att.Width != width || att.Height == nil || *att.Height != height {
		t.Errorf("dimensions came back as %v x %v", att.Width, att.Height)
	}
	if !att.HasThumbnail {
		t.Error("has_thumbnail did not survive the round trip")
	}

	read, err := w.store.AttachmentByID(w.ctx, att.ID)
	if err != nil {
		t.Fatalf("AttachmentByID: %v", err)
	}
	if read.ID != att.ID || read.Filename != att.Filename {
		t.Errorf("AttachmentByID returned %+v, want the row just written", read)
	}
	if _, err := w.store.AttachmentByID(w.ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("AttachmentByID of an unknown id = %v, want ErrNotFound", err)
	}
}

func TestCreateAttachmentInAMissingChannelIsNotFoundIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)

	// The channel went away between the membership check and the insert. A
	// foreign-key error would surface as a 500; the honest answer is the 404
	// a stranger would have got.
	_, err := w.store.CreateAttachment(w.ctx, storage.NewAttachment{
		ID:          uuid.New(),
		ChannelID:   uuid.New(),
		UploaderID:  w.people.uploader.ID,
		Filename:    "orphan.bin",
		ContentType: "application/octet-stream",
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("CreateAttachment into a missing channel = %v, want ErrNotFound", err)
	}
}

func TestSendClaimsItsAttachmentsIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	first := w.upload(t, w.channelID, w.people.uploader.ID, "one.bin")
	second := w.upload(t, w.channelID, w.people.uploader.ID, "two.bin")

	msg, created, err := w.send("here are two files", first.ID, second.ID)
	if err != nil || !created {
		t.Fatalf("send with attachments: created=%v err=%v", created, err)
	}

	for _, id := range []uuid.UUID{first.ID, second.ID} {
		got := w.messageIDOf(t, id)
		if got == nil || *got != msg.ID {
			t.Errorf("attachment %s points at %v, want message %s", id, got, msg.ID)
		}
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("the sent message carries %d attachments, want 2", len(msg.Attachments))
	}
	// Oldest upload first, so the cards render in the order they were added.
	if msg.Attachments[0].ID != first.ID || msg.Attachments[1].ID != second.ID {
		t.Errorf("attachments came back in the order %v", []uuid.UUID{
			msg.Attachments[0].ID, msg.Attachments[1].ID})
	}
}

func TestSendRefusesAnAttachmentItMayNotClaimIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)

	elsewhere := w.upload(t, w.otherChannelID, w.people.uploader.ID, "another-channel.bin")
	somebodyElses := w.upload(t, w.channelID, w.people.other.ID, "not-mine.bin")
	alreadySent := w.upload(t, w.channelID, w.people.uploader.ID, "spoken-for.bin")
	if _, _, err := w.send("the first message", alreadySent.ID); err != nil {
		t.Fatalf("seed the already-attached file: %v", err)
	}

	// Every miss is the same error. A caller that could tell them apart
	// could probe for other people's uploads by id.
	tests := map[string]uuid.UUID{
		"no such attachment": uuid.New(),
		"another channel's":  elsewhere.ID,
		"another person's":   somebodyElses.ID,
		"already attached":   alreadySent.ID,
		"the nil uuid":       uuid.Nil,
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			mine := w.upload(t, w.channelID, w.people.uploader.ID, "good.bin")

			_, _, err := w.send("text and a doomed file", mine.ID, id)
			if !errors.Is(err, storage.ErrAttachmentNotFound) {
				t.Fatalf("send = %v, want ErrAttachmentNotFound", err)
			}
			// All of them or none: the good file in the same list must not
			// have been quietly claimed by a send that failed.
			if got := w.messageIDOf(t, mine.ID); got != nil {
				t.Errorf("the valid attachment was claimed by the failed send (message %s)", *got)
			}
		})
	}
}

func TestSendRefusesAMessageWithNeitherTextNorFilesIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)

	if _, _, err := w.send(""); !errors.Is(err, storage.ErrEmptyMessage) {
		t.Errorf("empty send = %v, want ErrEmptyMessage", err)
	}

	// Empty content is legal exactly when a file comes with it — an image
	// with no caption is an ordinary message.
	att := w.upload(t, w.channelID, w.people.uploader.ID, "captionless.png")
	msg, created, err := w.send("", att.ID)
	if err != nil || !created {
		t.Fatalf("captionless send: created=%v err=%v", created, err)
	}
	if msg.Content != "" || len(msg.Attachments) != 1 {
		t.Errorf("captionless message came back as %q with %d attachments", msg.Content, len(msg.Attachments))
	}
}

func TestResendDoesNotReclaimAttachmentsIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	att := w.upload(t, w.channelID, w.people.uploader.ID, "sent-once.bin")
	other := w.upload(t, w.channelID, w.people.uploader.ID, "never-sent.bin")

	key := uuid.New()
	nm := storage.NewMessage{
		ChannelID:     w.channelID,
		AuthorID:      w.people.uploader.ID,
		ClientMsgID:   key,
		Content:       "one message",
		AttachmentIDs: []uuid.UUID{att.ID},
	}
	first, created, err := w.store.CreateMessage(w.ctx, nm)
	if err != nil || !created {
		t.Fatalf("first send: created=%v err=%v", created, err)
	}

	// The retry the contract promises is safe: same key, and this time the
	// client names a different file. Nothing may move — the stored message
	// is the one that exists.
	nm.AttachmentIDs = []uuid.UUID{other.ID}
	again, created, err := w.store.CreateMessage(w.ctx, nm)
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if created {
		t.Error("a resend of a taken idempotency key reported a new message")
	}
	if again.ID != first.ID {
		t.Errorf("resend returned message %s, want the stored %s", again.ID, first.ID)
	}
	if got := w.messageIDOf(t, other.ID); got != nil {
		t.Errorf("the resend claimed %s for message %s; a resend must claim nothing", other.ID, *got)
	}
	// The message's own attachments still come back on the resend, so a
	// client reconciling from the 200 renders the same cards.
	if len(again.Attachments) != 1 || again.Attachments[0].ID != att.ID {
		t.Errorf("resend returned %d attachments, want the one the message holds", len(again.Attachments))
	}
}

func TestHistoryCarriesAttachmentsIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	att := w.upload(t, w.channelID, w.people.uploader.ID, "in-history.png")
	if _, _, err := w.send("with a file", att.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, _, err := w.send("without one"); err != nil {
		t.Fatalf("send: %v", err)
	}

	page, err := w.store.ListMessages(w.ctx, storage.ListMessagesParams{
		ChannelID: w.channelID, Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("history holds %d messages, want 2", len(page.Messages))
	}
	if len(page.Messages[0].Attachments) != 1 || page.Messages[0].Attachments[0].ID != att.ID {
		t.Errorf("the message with a file came back with %d attachments", len(page.Messages[0].Attachments))
	}
	if len(page.Messages[1].Attachments) != 0 {
		t.Errorf("a message with no files came back with %d attachments", len(page.Messages[1].Attachments))
	}
}

func TestAttachmentsByMessagesGroupsAndIgnoresUnattachedIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	one := w.upload(t, w.channelID, w.people.uploader.ID, "a.bin")
	two := w.upload(t, w.channelID, w.people.uploader.ID, "b.bin")
	loose := w.upload(t, w.channelID, w.people.uploader.ID, "still-in-the-composer.bin")

	first, _, err := w.send("first", one.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	second, _, err := w.send("second", two.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	byMessage, err := w.store.AttachmentsByMessages(w.ctx, []uuid.UUID{first.ID, second.ID, uuid.New()})
	if err != nil {
		t.Fatalf("AttachmentsByMessages: %v", err)
	}
	if len(byMessage) != 2 {
		t.Fatalf("grouped into %d messages, want 2", len(byMessage))
	}
	if byMessage[first.ID][0].ID != one.ID || byMessage[second.ID][0].ID != two.ID {
		t.Error("attachments were grouped under the wrong messages")
	}
	for _, atts := range byMessage {
		if slices.ContainsFunc(atts, func(a storage.Attachment) bool { return a.ID == loose.ID }) {
			t.Error("an unattached upload came back with a message's cards")
		}
	}

	empty, err := w.store.AttachmentsByMessages(w.ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("AttachmentsByMessages(nil) = %v, %v", empty, err)
	}
}

func TestSweepOrphanAttachmentsIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	fresh := w.upload(t, w.channelID, w.people.uploader.ID, "just-uploaded.bin")
	stale := w.upload(t, w.channelID, w.people.uploader.ID, "abandoned.bin")
	attached := w.upload(t, w.channelID, w.people.uploader.ID, "sent.bin")
	if _, _, err := w.send("sent with a file", attached.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Age the abandoned one past the window. created_at is stamped by the
	// database, so backdating is the only way to test the boundary.
	if _, err := w.conn.Exec(w.ctx,
		`UPDATE attachments SET created_at = now() - interval '25 hours' WHERE id = $1`, stale.ID,
	); err != nil {
		t.Fatalf("backdate the orphan: %v", err)
	}

	swept, err := w.store.SweepOrphanAttachments(w.ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("SweepOrphanAttachments: %v", err)
	}
	if !slices.Equal(swept, []uuid.UUID{stale.ID}) {
		t.Fatalf("swept %v, want exactly the abandoned upload %s", swept, stale.ID)
	}

	// The ids come back so the caller can delete the blobs; the rows are
	// already gone.
	if _, err := w.store.AttachmentByID(w.ctx, stale.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the swept row is still there: %v", err)
	}
	for _, id := range []uuid.UUID{fresh.ID, attached.ID} {
		if _, err := w.store.AttachmentByID(w.ctx, id); err != nil {
			t.Errorf("the sweep took an attachment it should not have (%s): %v", id, err)
		}
	}
}

func TestConcurrentSendsClaimOneAttachmentOnceIntegration(t *testing.T) {
	t.Parallel()

	w := newAttachmentWorld(t)
	contested := w.upload(t, w.channelID, w.people.uploader.ID, "contested.bin")

	// Two sends racing for the same file. The claim locks its rows in id
	// order inside the transaction, so one wins and the other finds the row
	// taken — never a deadlock, and never two messages holding one file.
	const senders = 8
	results := make(chan error, senders)
	for range senders {
		go func() {
			_, _, err := w.send("mine", contested.ID)
			results <- err
		}()
	}

	won := 0
	for range senders {
		switch err := <-results; {
		case err == nil:
			won++
		case errors.Is(err, storage.ErrAttachmentNotFound):
		default:
			t.Errorf("racing send failed with %v, want success or ErrAttachmentNotFound", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of %d racing sends claimed the file, want exactly 1", won, senders)
	}
}
