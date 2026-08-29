package api

import (
	"encoding/base64"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// This file is the single mapping from a stored message onto the contract's
// Message, shared by the REST handlers and the WebSocket gateway.
//
// It lives here because it was duplicated, and the duplicate drifted: the
// gateway's copy hard-coded an empty attachments array and knew nothing
// about link previews, so a message with files arrived live carrying no
// cards and an enrichment announced a preview the frame did not contain.
// Two serializations of one contract type is one too many; the type is
// generated here, so the mapping onto it belongs here too.

// FileURLSigner mints the signed, expiring URLs an Attachment carries.
// Implemented by internal/filesign. It is a parameter rather than a field
// because a URL is minted per serialization, for the reader being served —
// the URL is the credential (ADR 003), so it is never cached or stored.
type FileURLSigner interface {
	AttachmentURLs(id uuid.UUID, hasThumbnail bool) (fileURL string, thumbnailURL *string)
}

// MessageOf maps a stored message onto the contract's Message.
//
// A nil signer yields attachments with empty URLs rather than no attachments:
// the cards still say what was shared, which is the honest degradation for a
// server with no signing key. Callers that have a signer must pass it.
func MessageOf(m storage.Message, signer FileURLSigner) Message {
	return Message{
		Id:        m.ID,
		ChannelId: m.ChannelID,
		Author: UserSummary{
			Id:          m.Author.ID,
			Username:    m.Author.Username,
			DisplayName: m.Author.DisplayName,
		},
		ClientMsgId: m.ClientMsgID,
		Content:     m.Content,
		CreatedAt:   m.CreatedAt,
		EditedAt:    m.EditedAt,
		DeletedAt:   m.DeletedAt,
		Attachments: AttachmentsOf(m.Attachments, signer),
		LinkPreview: LinkPreviewOf(m.Preview, signer),
		Mls:         MlsEnvelopeOf(m.Mls),
	}
}

// MlsEnvelopeOf maps a message's ciphertext onto the contract's envelope, or
// nil when the message carries none.
//
// The envelope is present exactly when the stored columns are, and there is
// only this one mapping: a message of an e2ee channel therefore cannot be
// serialized without it, and a deleted one — whose columns the soft delete
// cleared — cannot be serialized with it. Base64 in JSON, bytea in storage,
// converted here and nowhere else.
func MlsEnvelopeOf(mls *storage.MessageMls) *MlsMessageEnvelope {
	if mls == nil {
		return nil
	}
	return &MlsMessageEnvelope{
		Epoch:      mls.Epoch,
		Ciphertext: base64.StdEncoding.EncodeToString(mls.Ciphertext),
	}
}

// AttachmentsOf maps a message's file cards. Always a slice, never nil: the
// contract makes attachments required, and a JSON null there is a client
// crash rather than an empty list.
func AttachmentsOf(atts []storage.Attachment, signer FileURLSigner) []Attachment {
	out := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, AttachmentOf(a, signer))
	}
	return out
}

// AttachmentOf maps one file card, minting its URLs.
func AttachmentOf(a storage.Attachment, signer FileURLSigner) Attachment {
	out := Attachment{
		Id:          a.ID,
		Filename:    a.Filename,
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
		Width:       a.Width,
		Height:      a.Height,
	}
	if signer != nil {
		out.Url, out.ThumbnailUrl = signer.AttachmentURLs(a.ID, a.HasThumbnail)
	}
	return out
}

// LinkPreviewOf maps a stored preview card, or nil when the message has
// none.
//
// image_url is minted here from this instance's own signer — never stored,
// and never the remote site's URL. A reader's browser must not be made to
// fetch a stranger's server (ADR 003).
func LinkPreviewOf(preview *storage.LinkPreview, signer FileURLSigner) *LinkPreview {
	if preview == nil {
		return nil
	}
	out := &LinkPreview{Url: preview.URL}
	if preview.Title != "" {
		title := preview.Title
		out.Title = &title
	}
	if preview.Description != "" {
		description := preview.Description
		out.Description = &description
	}
	if preview.ImageBlobID != uuid.Nil && signer != nil {
		imageURL, _ := signer.AttachmentURLs(preview.ImageBlobID, false)
		out.ImageUrl = &imageURL
	}
	return out
}
