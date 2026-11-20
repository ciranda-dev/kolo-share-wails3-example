package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolofiles "github.com/ciranda-dev/kolo-files"
	invite "github.com/ciranda-dev/kolo-invite"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// Sentinels of the app, for errors.Is. Every error a window sees is a *ShareError that wraps one of them or an
// error of a Kolo library.
var (
	ErrOwnCode        = errors.New("kolo-share: own pairing code")
	ErrBadFolder      = errors.New("kolo-share: bad folder")
	ErrBusy           = errors.New("kolo-share: busy")
	ErrNotFound       = errors.New("kolo-share: other computer not found")
	ErrTooLarge       = errors.New("kolo-share: over the download limit")
	ErrNotReady       = errors.New("kolo-share: network not ready")
	ErrIncomplete     = errors.New("kolo-share: item incomplete")
	ErrWrongKind      = errors.New("kolo-share: item does not match its kind")
	ErrOfferMismatch  = errors.New("kolo-share: item does not match its offer")
	ErrNameCollision  = errors.New("kolo-share: two names are one on this disk")
	ErrSource         = errors.New("kolo-share: cannot send this")
	ErrNotSaved       = errors.New("kolo-share: not a path this app saved")
	ErrTooManyFiles   = errors.New("kolo-share: over the file limit")
	ErrTooManyEntries = errors.New("kolo-share: over the log entry limit")
	ErrClosed         = errors.New("kolo-share: closed")
	ErrPeerProtocol   = errors.New("kolo-share: peer broke the control protocol")
	ErrOffline        = errors.New("kolo-share: session offline")
	ErrSessionEnded   = errors.New("kolo-share: session ended")
	ErrOfferGone      = errors.New("kolo-share: no such offer")
	ErrBadName        = errors.New("kolo-share: bad name")
	ErrNameLength     = errors.New("kolo-share: name too long for this system")
	ErrPathLength     = errors.New("kolo-share: path too long for this system")
)

// Kinds of ShareError: broad groups, as the window sees them.
const (
	kindInvalidCode = "invalid-code" // a pairing code that cannot be used
	kindBadFolder   = "bad-folder"
	kindBusy        = "busy"
	kindNotFound    = "not-found"
	kindRefused     = "refused"
	kindCorrupt     = "corrupt"
	kindTooLarge    = "too-large"
	kindUnsafeName  = "unsafe-name"
	kindLimit       = "limit"
	kindDisk        = "disk"
	kindLost        = "lost"
	kindNetwork     = "network"
	kindConfig      = "config"
	kindSource      = "source"
	kindCancelled   = "cancelled"
	kindInternal    = "internal"
)

// reasons gives every error sentence a stable ID, its kind and its English text. {detail} is a path, a name, a
// number or a setting, and {cause} the plain words of the underlying error. A window in another language
// translates by reason and fills in the same two values; the English Message is the fallback.
var reasons = map[string]struct{ kind, text string }{
	"cancelled":               {kindCancelled, "Cancelled. Nothing was saved."},
	"resume-gave-up":          {kindLost, "Nothing came from the other computer for {detail}, so the transfer stopped. Nothing was saved."},
	"too-large":               {kindTooLarge, "This item is larger than the download limit of {detail}. Nothing was saved."},
	"unsafe-name":             {kindUnsafeName, "The item has a name this computer cannot save safely: {detail}. Nothing was saved."},
	"name-length":             {kindUnsafeName, "The item has a name of {detail} bytes, longer than the {cause} bytes this computer can save. Nothing was saved."},
	"path-length":             {kindUnsafeName, "The item has a path of {detail} bytes in the folder you chose, longer than the {cause} bytes this computer can open. Choose a folder with a shorter path. Nothing was saved."},
	"name-collision":          {kindUnsafeName, "Two names in the item are the same name on this disk ({detail}), for example names that differ only in case. Nothing was saved."},
	"too-many-files":          {kindTooLarge, "This item has more than {detail} files and folders, the limit of this app. Nothing was saved."},
	"too-many-entries":        {kindTooLarge, "The data from the other computer has more parts than an item of {detail} files and folders can have. Nothing was saved."},
	"source-too-many":         {kindSource, "This folder has more than {detail} files and folders, more than one item may hold. Send a smaller folder."},
	"busy-restarting":         {kindBusy, "The network is restarting. Try again in a moment."},
	"app-closed":              {kindBusy, "Kolo Share is closing. Start it again to connect."},
	"data-dir-open":           {kindConfig, "The data directory {detail} is open to other users. Make it private (chmod 700) or choose another one."},
	"data-dir-foreign":        {kindConfig, "The data directory {detail} holds a transfers folder that Kolo Share did not make. Choose another data directory."},
	"too-deep":                {kindLimit, "The folder is too deep or has a name that is too long. Nothing was saved."},
	"corrupt":                 {kindCorrupt, "The data from the other computer failed verification. Nothing was saved."},
	"lost":                    {kindLost, "The connection to the other computer was lost. Nothing was saved."},
	"network-not-ready":       {kindNetwork, "The network is not ready yet. Wait a moment, or press Try again."},
	"disk":                    {kindDisk, "Could not write {detail}: {cause}."},
	"disk-full":               {kindDisk, "The disk filled up while writing {detail}. Free some space and try again. Nothing was saved."},
	"no-space-prepare":        {kindDisk, "Not enough free disk space to prepare this item: it needs {detail} in the data folder and only {cause} is free. Free some space and send it again."},
	"no-space-download":       {kindDisk, "Not enough free disk space to download this item: it needs {detail} in the data folder while it downloads and only {cause} is free. Nothing was saved."},
	"no-space-save":           {kindDisk, "Not enough free disk space in the folder you chose: the item needs {detail} and only {cause} is free. Nothing was saved."},
	"no-space-both":           {kindDisk, "Not enough free disk space: this item needs {detail} on this disk, a copy while it downloads and the saved files, and only {cause} is free. Choose a folder on another disk, or free some space. Nothing was saved."},
	"internal":                {kindInternal, "Something went wrong: {cause}."},
	"busy-reconnect":          {kindBusy, "Close every session and cancel your pairing code first."},
	"source-missing":          {kindSource, "Choose a file or a folder to send."},
	"source-unreadable":       {kindSource, "Could not read {detail}: {cause}."},
	"source-link":             {kindSource, "{detail} is a link. Send the file or folder it points to."},
	"source-special":          {kindSource, "{detail} is not a file or a folder."},
	"source-root":             {kindSource, "Choose a file or a folder inside a drive, not the drive itself."},
	"source-normalization":    {kindSource, "{detail} has a name that is the same as another one in Unicode normalization. Rename one of them."},
	"source-store":            {kindSource, "Could not prepare {detail}: {cause}."},
	"network-open":            {kindNetwork, "Could not open the network: {cause}."},
	"entry-nodes-unreachable": {kindNetwork, "Could not reach the entry nodes: {cause}. Check the network this app is set to use: -bootstrap or -local."},
	"lookup-failed":           {kindNetwork, "Could not look for the other computer: {cause}."},
	"folder-missing":          {kindBadFolder, "Choose a folder to save into."},
	"folder-relative":         {kindBadFolder, "Choose a folder to save into, with its full path."},
	"folder-unusable":         {kindBadFolder, "The folder {detail} cannot be used: {cause}."},
	"folder-not-dir":          {kindBadFolder, "{detail} is not a folder."},
	"copies-exhausted":        {kindDisk, "The folder already holds {detail} and 99 copies of it. Choose another folder."},
	"config-setting":          {kindConfig, "The setting -{detail} {cause}."},
	"data-dir":                {kindConfig, "Could not open the data directory: {cause}."},
	"no-dialogs":              {kindInternal, "This window cannot open dialogs."},
	"dialog-failed":           {kindInternal, "Could not open the dialog: {cause}."},
	"reveal-refused":          {kindBadFolder, "Kolo Share shows only what it saved."},
	"reveal-failed":           {kindInternal, "Could not show {detail}: {cause}."},
	"name-too-long":           {kindConfig, "A name has at most {detail} characters."},
	// Pairing.
	"pair-invalid-code": {kindInvalidCode, "This is not a pairing code. Copy the whole code, it starts with koloinv0."},
	"pair-not-found":    {kindNotFound, "Could not find the computer that made this code. Check that its app is open and shows the code, then try again."},
	"pair-expired":      {kindRefused, "This code expired. Ask for a new one."},
	"pair-used":         {kindRefused, "This code was already used. Ask for a new one."},
	"pair-revoked":      {kindRefused, "The other person cancelled this code. Ask for a new one."},
	"pair-denied":       {kindRefused, "The other computer did not accept this code. It may have too many sessions open."},
	"pair-own-code":     {kindInvalidCode, "This is your own code. Send it to the other person."},
	"pair-rate-limited": {kindBusy, "Too many tries. Wait a minute and try again."},
	"pair-invalid":      {kindRefused, "The computer that made this code does not know it. Ask for a new code."},
	"pair-lost":         {kindLost, "The connection broke while connecting. Try again."},
	"busy-pairing":      {kindBusy, "A code is already waiting. Use it or cancel it first."},
	"busy-sessions":     {kindBusy, "You have {detail} sessions open, the most this app keeps. Close one first."},
	// Sessions and the items in them.
	"busy-offers":     {kindBusy, "{detail} items already wait for an answer in this session. Wait for one first."},
	"autosave-limit":  {kindLimit, "Auto-save is off now: this session saved half of the free space the folder had when you turned it on. This item waits for your answer. Turn auto-save on again to go on."},
	"offer-limit":     {kindLimit, "Declined: the other computer offered more items than this app takes at once."},
	"offer-mismatch":  {kindCorrupt, "What arrived did not match what was offered. Nothing was saved."},
	"offer-withdrawn": {kindCancelled, "The other computer cancelled this item. Nothing was saved."},
	"offer-gone":      {kindRefused, "This item is not waiting for an answer any more."},
	"peer-failed":     {kindRefused, "The other computer could not save it."},
	"prepare-failed":  {kindRefused, "The other computer could not prepare this item."},
	"prepare-stalled": {kindLost, "Nothing came from the other computer while it prepared this item, so this app stopped waiting."},
	"peer-protocol":   {kindRefused, "The other computer sent something this app does not understand, so the session was closed."},
	"session-offline": {kindLost, "The other computer is offline. Wait until the session is connected again."},
	"session-ended":   {kindCancelled, "This session is closed."},
	// Relays, in Network.relays.
	"relay-pairing-not-allowed": {kindNetwork, "The relay {detail} has an allow list, so a pairing code cannot wait there. The other computer can find your code only directly."},
	"relay-not-allowed":         {kindNetwork, "The relay {detail} does not let Kolo Share hold a place there. Ask its operator to allow your key (the kolo-id line in the terminal), or start Kolo Share without it."},
	"relay-full":                {kindNetwork, "The relay {detail} is full. Kolo Share tries again; your other relays still work."},
	"relay-wrong-key":           {kindNetwork, "The relay at {detail} answered with another key. Check its key in -relay, -relay-file or KOLO_SHARE_RELAYS."},
	"relay-bad-key":             {kindNetwork, "The key of the relay {detail} cannot be used. Fix it in -relay, -relay-file or KOLO_SHARE_RELAYS."},
	"relay-unreachable":         {kindNetwork, "Could not reach the relay {detail}. Kolo Share tries again."},
	// Network states, in Network.reason.
	"public-unreachable":  {kindNetwork, "Could not reach the public network: {cause}. Check your internet connection, or start Kolo Share with -bootstrap and the entry nodes of your own network, or with -local to share on this computer only."},
	"first-node-failed":   {kindNetwork, "Could not start the first entry node on {detail}: {cause}."},
	"bootstrap-failed":    {kindNetwork, "Could not reach the entry nodes ({detail}): {cause}. Check the list given with -bootstrap."},
	"local-port-taken":    {kindNetwork, "Another program uses {detail} on this computer, so the local network cannot start. Close that program and press Try again, or start Kolo Share without -local to use the public network, or with -bootstrap and the entry nodes of your own network."},
	"no-local-entry-node": {kindNetwork, "No entry node answered at {detail}. Start the first window with -first and that address, then press Try again."},
}

// ShareError is what the window shows. Reason is the stable ID of the sentence and Kind its group; Detail and
// Cause fill the sentence; Message is the sentence in English. Error returns Message, so a rejected binding
// carries it; Unwrap gives the cause for errors.Is.
type ShareError struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail"`
	Cause   string `json:"cause"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *ShareError) Error() string { return e.Message }
func (e *ShareError) Unwrap() error { return e.Err }

// shareErr makes the ShareError of reason. cause is the plain text of err when err is not one of the app's
// sentinels, so a sentence never shows "kolo-share: ...".
func shareErr(reason string, err error, detail string) *ShareError {
	cause := ""
	if err != nil {
		cause = plainCause(err)
	}
	return &ShareError{Kind: reasons[reason].kind, Reason: reason, Detail: detail, Cause: cause, Message: sentence(reason, detail, cause), Err: err}
}

// sentence fills the English text of reason.
func sentence(reason, detail, cause string) string {
	return strings.NewReplacer("{detail}", detail, "{cause}", cause).Replace(reasons[reason].text)
}

// explain turns any error of a transfer into a ShareError. lim gives the limits, for their sentences.
func explain(err error, lim limits) *ShareError {
	var se *ShareError
	var pe *fs.PathError
	path := ""
	if errors.As(err, &pe) {
		path = pe.Path
	}
	switch {
	case err == nil:
		return nil
	case errors.As(err, &se):
		return se
	case errors.Is(err, invite.ErrInvalidCode):
		return shareErr("pair-invalid-code", err, "")
	case errors.Is(err, ErrOwnCode):
		return shareErr("pair-own-code", err, "")
	case errors.Is(err, ErrNotFound):
		return shareErr("pair-not-found", err, "")
	case errors.Is(err, invite.ErrExpired):
		return shareErr("pair-expired", err, "")
	case errors.Is(err, invite.ErrUsedUp):
		return shareErr("pair-used", err, "")
	case errors.Is(err, invite.ErrRevoked):
		return shareErr("pair-revoked", err, "")
	case errors.Is(err, invite.ErrDenied):
		return shareErr("pair-denied", err, "")
	case errors.Is(err, invite.ErrRateLimited), errors.Is(err, invite.ErrBusy):
		return shareErr("pair-rate-limited", err, "")
	case errors.Is(err, invite.ErrInvalid), errors.Is(err, invite.ErrTooLarge), errors.Is(err, invite.ErrProtocol):
		return shareErr("pair-invalid", err, "")
	case errors.Is(err, invite.ErrUnreachable), errors.Is(err, invite.ErrNotAnswered):
		return shareErr("pair-lost", err, "")
	case errors.Is(err, context.Canceled):
		return shareErr("cancelled", err, "")
	case errors.Is(err, kolosync.ErrNotFound):
		return shareErr("offer-withdrawn", err, "") // a sender of this app refuses a log only once its offer ended
	case errors.Is(err, ErrOfferMismatch):
		return shareErr("offer-mismatch", err, "")
	case errors.Is(err, ErrPeerProtocol):
		return shareErr("peer-protocol", err, "")
	case errors.Is(err, ErrOffline):
		return shareErr("session-offline", err, "")
	case errors.Is(err, ErrSessionEnded):
		return shareErr("session-ended", err, "")
	case errors.Is(err, ErrOfferGone):
		return shareErr("offer-gone", err, "")
	case errors.Is(err, ErrTooLarge), errors.Is(err, kolosync.ErrTooLarge):
		return shareErr("too-large", err, humanBytes(lim.bytes))
	case diskFull(err):
		return shareErr("disk-full", ErrNoSpace, path)
	case errors.Is(err, ErrTooManyFiles):
		return shareErr("too-many-files", err, strconv.Itoa(lim.nodes))
	case errors.Is(err, ErrTooManyEntries):
		return shareErr("too-many-entries", err, strconv.Itoa(lim.nodes))
	case errors.Is(err, kolofiles.ErrUnsafeName), pe != nil && pe.Op == "localpath":
		return shareErr("unsafe-name", err, cleanName(path))
	case errors.Is(err, ErrNameCollision), errors.Is(err, fs.ErrExist):
		return shareErr("name-collision", err, path)
	case errors.Is(err, kolofiles.ErrLimit):
		return shareErr("too-deep", err, "")
	case errors.Is(err, koloblobs.ErrCorrupt), errors.Is(err, kolofiles.ErrCorrupt), errors.Is(err, kolostore.ErrCorrupt),
		errors.Is(err, kolosync.ErrInvalid), errors.Is(err, kolosync.ErrDiverged), errors.Is(err, kolosync.ErrProtocol),
		errors.Is(err, koloblobs.ErrWrongKey), errors.Is(err, koloblobs.ErrNotFound), errors.Is(err, kololog.ErrKeyUnavailable),
		errors.Is(err, ErrIncomplete), errors.Is(err, ErrWrongKind), errors.Is(err, fs.ErrInvalid):
		return shareErr("corrupt", err, "")
	case errors.Is(err, kolosync.ErrTimeout), errors.Is(err, kolosync.ErrBusy), errors.Is(err, koloswarm.ErrLost),
		errors.Is(err, koloswarm.ErrClosed), errors.Is(err, kolosync.ErrBehind), errors.Is(err, kolotransport.ErrPeerClosed),
		errors.Is(err, kolotransport.ErrClosed), errors.Is(err, kolotransport.ErrTimeout), errors.Is(err, kolotransport.ErrStreamReset),
		errors.Is(err, kolotransport.ErrStreamCanceled), errors.Is(err, kolotransport.ErrTransport):
		return shareErr("lost", err, "")
	case errors.Is(err, ErrNotReady):
		return shareErr("network-not-ready", err, "")
	case pe != nil:
		se := shareErr("disk", err, pe.Path)
		se.Cause = plainCause(pe.Err)
		se.Message = sentence("disk", pe.Path, se.Cause)
		return se
	}
	return shareErr("internal", err, "")
}

// plainCause is the text of err for a sentence: the words of the OS for the common cases, without the
// "kolo-share: " of the app's sentinels.
func plainCause(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "it does not exist"
	}
	return strings.TrimPrefix(err.Error(), "kolo-share: ")
}

// humanDuration writes d in seconds, minutes or hours, such as "30 min".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	}
	return fmt.Sprintf("%d h %d min", int(d/time.Hour), int((d%time.Hour).Round(time.Minute)/time.Minute))
}

// humanBytes writes n in the largest binary unit that keeps it at least 1, such as "32 GiB".
func humanBytes(n int64) string {
	units := []string{"bytes", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d bytes", n)
	}
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d %s", int64(f), units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
