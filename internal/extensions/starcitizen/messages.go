package starcitizen

import (
	"errors"
	"fmt"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
)

// Why the helper refuses: the codes of its extensions.Refusal errors. The
// person reads each one's sentence (MessageText): on the card as Install's
// or Remove's reason or as a status line, and from the dispatcher's record
// of a refused launch. The sentences name no drive, so a code means the
// same wherever it comes from; the card's status names the drive.
const (
	codeNotConnected     = "drive-not-connected" // a game drive not mounted at its folder, or holding another filesystem
	codeFilesMissing     = "files-missing"       // the drive is there and the files aren't
	codeFilesElsewhere   = "files-elsewhere"     // no prefix recorded, or one in no place VaporOS uses (fetch-installer: not the recorded one)
	codeSettingUp        = "setting-up"          // a start before Install recorded an installer
	codeInstallerMissing = "installer-missing"
	codeNoDrive          = "no-drive"
	codeUnknownDrive     = "unknown-drive"
	codeWrongFS          = "wrong-filesystem"
	codeNoSpace          = "no-space"
	codeCantWrite        = "cant-write"
	codeFeed             = "feed-unreachable"
	codeDownload         = "download-failed"
	codeMismatch         = "installer-mismatch"
	codeFilesStay        = "files-stay" // a purge while the drive isn't connected
	codeCantDelete       = "cant-delete"
)

// fetchCodes are the codes fetch-installer prints, and the only ones
// Install takes from it.
var fetchCodes = map[string]bool{
	codeNotConnected: true, codeFilesMissing: true, codeFilesElsewhere: true, codeCantWrite: true,
	codeFeed: true, codeDownload: true, codeMismatch: true,
}

// MessageText words the codes of the helper's refusals
// (extensions.MessageWords).
func (helper) MessageText(code string) (string, bool) { return messageText(code) }

func messageText(code string) (string, bool) {
	switch code {
	case codeNotConnected:
		return "Star Citizen's drive isn't connected. Connect it, then try again.", true
	case codeFilesMissing:
		return "Star Citizen's files are missing from its drive. Remove Star Citizen and add it again.", true
	case codeFilesElsewhere:
		return "Star Citizen's files aren't where VaporOS put them. Remove Star Citizen and add it again.", true
	case codeSettingUp:
		return "Star Citizen is still being set up. Start it again once its card says it's installed.", true
	case codeInstallerMissing:
		return "Star Citizen's installer is missing. Remove Star Citizen and add it again.", true
	case codeNoDrive:
		return "Pick a game drive for Star Citizen on its card, then select Try again.", true
	case codeUnknownDrive:
		return "VaporOS doesn't know the drive picked for Star Citizen. Pick another one on its card, then select Try again.", true
	case codeWrongFS:
		return fmt.Sprintf("Star Citizen can't run from the drive picked for it because of how that drive is formatted. Pick a drive formatted as %s, then select Try again.", orList(games(nil).FS)), true
	case codeNoSpace:
		return "The drive picked for Star Citizen doesn't have enough free space. Free up space or pick another drive, then select Try again.", true
	case codeCantWrite:
		return "VaporOS couldn't write to Star Citizen's drive. Check that it has space, then try again.", true
	case codeFeed:
		return "The RSI Launcher's download page didn't answer. Check the internet connection, then try again.", true
	case codeDownload:
		return "The RSI Launcher's installer didn't download. Check the internet connection, then try again.", true
	case codeMismatch:
		return "The RSI Launcher's installer didn't match the fingerprint its publisher lists, so VaporOS deleted it. Try again later.", true
	case codeFilesStay:
		return "Star Citizen's drive isn't connected, so its files stay on it.", true
	case codeCantDelete:
		return "VaporOS couldn't delete Star Citizen's files from its drive, so they stay on it.", true
	}
	return "", false
}

// refuse is a refusal with code; the format and args say, for the
// journal only, what was found.
func refuse(code, format string, args ...any) error {
	return extensions.Refuse(code, fmt.Errorf(format, args...))
}

// codeOf is err's refusal code, "" for any other error.
func codeOf(err error) string {
	var r *extensions.Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// said is what the person reads for err: its code's sentence, "" for an
// error the helper has no words for.
func said(err error) string {
	text, _ := messageText(codeOf(err))
	return text
}
