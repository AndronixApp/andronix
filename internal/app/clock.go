package app

import (
	"crypto/x509"
	"errors"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// clockFloor is a date this installer can't run before: it was built after
// it. A phone whose clock reads earlier is wrong, and every package
// signature check then fails whatever the mirror — gpg rejects signing keys
// "created in the future" (pacman: "invalid or corrupted package (PGP
// signature)"; apt: "InRelease is not signed", then "unauthenticated
// packages"), card #354. Moving mirrors or retrying can't fix that, so the
// install stops and says to fix the date. Bump it now and then; it only
// has to stay before the release date.
var clockFloor = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

// clockNow is time.Now (a variable for tests).
var clockNow = time.Now

// clockBehind reports the phone's time and whether it is before clockFloor.
func clockBehind() (time.Time, bool) {
	now := clockNow()
	return now, now.Before(clockFloor)
}

// clockCertErr: err is a TLS certificate rejected as expired or not yet
// valid on a phone whose clock is behind clockFloor. Then the date is
// wrong, not the network or the server: with the phone at 2016 every
// download server's certificate looks "not yet valid", and the install
// stopped at "Couldn't reach the download servers ... Check your internet
// connection", which sent people looking at their Wi-Fi.
func clockCertErr(err error) (*ui.UserError, bool) {
	if err == nil {
		return nil, false
	}
	var ce x509.CertificateInvalidError
	if !(errors.As(err, &ce) && ce.Reason == x509.Expired) &&
		!strings.Contains(err.Error(), "certificate has expired or is not yet valid") {
		return nil, false
	}
	now, behind := clockBehind()
	if !behind {
		return nil, false
	}
	return &ui.UserError{
		Title:  "Your phone's date is wrong",
		Class:  "clock_wrong",
		Kind:   string(pkgmgr.KindClock),
		Detail: clockDetail(now),
		What:   "The phone's date is " + now.Format("2 January 2006") + ". With the date that far back, the download servers' security certificates look invalid, so nothing can be downloaded.",
		Fix:    "Make sure the phone's date and time are set automatically (Settings, Date and time), then run the same command again.",
		Err:    err,
	}, true
}
