package index

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A level's capture URL has a fixed shape:
//
//	<prefix><id, 11 digits>-<revision, 5 digits>?Expires=<unix time>&Signature=<signature>&Key-Pair-Id=<key pair>
//
// The prefix and key pair are the same for (almost) every level, so they are stored once in small tables; the
// signature (256 random bytes, ~342 characters of CloudFront-flavoured base64) is the bulk of a row and is kept as
// raw bytes when it decodes cleanly.
var captureRe = regexp.MustCompile(`^(https?://[^?]*/)(\d{11})-(\d{5})\?Expires=(\d+)&Signature=([A-Za-z0-9~_.\-+/=]+)&Key-Pair-Id=([A-Za-z0-9]+)$`)

type capture struct {
	Prefix  string
	ID      int64
	Rev     int64
	Expires int64
	Sig     []byte
	SigRaw  bool // true: Sig is the signature text as it was written (it did not survive a decode/encode round trip)
	KeyPair string
}

// CloudFront writes base64 with '+' -> '-', '=' -> '_', '/' -> '~'.
var cloudFrontToStd = strings.NewReplacer("-", "+", "_", "=", "~", "/")
var stdToCloudFront = strings.NewReplacer("+", "-", "=", "_", "/", "~")

func parseCapture(u string) (capture, error) {
	m := captureRe.FindStringSubmatch(u)
	if m == nil {
		return capture{}, errors.New("capture URL does not have the expected shape")
	}

	id, _ := strconv.ParseInt(m[2], 10, 64)
	rev, _ := strconv.ParseInt(m[3], 10, 64)

	exp, err := strconv.ParseInt(m[4], 10, 64)
	if err != nil {
		return capture{}, fmt.Errorf("bad Expires: %w", err)
	}

	c := capture{Prefix: m[1], ID: id, Rev: rev, Expires: exp, KeyPair: m[6]}

	if raw, err := base64.StdEncoding.DecodeString(cloudFrontToStd.Replace(m[5])); err == nil && stdToCloudFront.Replace(base64.StdEncoding.EncodeToString(raw)) == m[5] {
		c.Sig = raw
	} else {
		c.Sig, c.SigRaw = []byte(m[5]), true
	}

	return c, nil
}

func (c capture) URL() string {
	sig := string(c.Sig)
	if !c.SigRaw {
		sig = stdToCloudFront.Replace(base64.StdEncoding.EncodeToString(c.Sig))
	}

	return fmt.Sprintf("%s%011d-%05d?Expires=%d&Signature=%s&Key-Pair-Id=%s", c.Prefix, c.ID, c.Rev, c.Expires, sig, c.KeyPair)
}
