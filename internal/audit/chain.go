package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// The chain is hash = sha256(prev_hash || canonical(event)). Canonical means a
// fixed field order, separator and number formatting, so the digest can be
// recomputed from a row read back out of PostgreSQL months later, by a different
// binary, on a different platform.
//
// The hash of the row itself is excluded from its own input (an obviously
// circular definition) and prev_hash is supplied explicitly.
const fieldSeparator = "\x1f"

// Canonical serialises the hashable part of an event.
func Canonical(e Event) []byte {
	var b strings.Builder
	write := func(s string) {
		b.WriteString(s)
		b.WriteString(fieldSeparator)
	}
	write(e.EventID)
	write(e.Time.UTC().Format(time.RFC3339Nano))
	write(e.RequestID)
	write(e.Source)
	write(e.ActorKind)
	write(e.ActorID)
	write(e.ActorLabel)
	write(e.OwnerID)
	write(e.IP)
	write(e.Method)
	write(e.Path)
	write(e.Action)
	write(e.ResourceID)
	write(e.ResourceKind)
	write(strconv.Itoa(e.Status))
	write(e.ErrorCode)
	write(strconv.FormatBool(e.ReadOnly))
	write(strconv.Itoa(e.LatencyMS))
	write(e.UserAgent)
	write(canonicalMetadata(e.Metadata))
	return []byte(b.String())
}

// canonicalMetadata renders the metadata map with sorted keys, which
// encoding/json does by contract, so the same map always hashes the same way.
func canonicalMetadata(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Hash returns the chain link for e given the previous link.
func Hash(prev string, e Event) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte(fieldSeparator))
	h.Write(Canonical(e))
	return hex.EncodeToString(h.Sum(nil))
}

// Verification is the answer of a chain check.
type Verification struct {
	Checked int64 `json:"checked"`
	OK      bool  `json:"ok"`
	// BrokenAt is the event id of the first row whose hash does not match, or
	// whose prev_hash does not match its predecessor.
	BrokenAt string `json:"brokenAt,omitempty"`
	BrokenID int64  `json:"brokenId,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Anchored says the walk did not begin at the start of history: the trail
	// was pruned, or a time window was supplied. AnchorAt is the first row
	// checked and AnchorSource is where its seed came from.
	Anchored     bool   `json:"anchored,omitempty"`
	AnchorAt     string `json:"anchorAt,omitempty"`
	AnchorSource string `json:"anchorSource,omitempty"`
}

const (
	// AnchorRetention is a seed recorded by the retention job when it removed
	// the predecessor of the current chain head. It is trusted: the head must
	// point at exactly this hash.
	AnchorRetention = "retention"
	// AnchorInferred takes the seed from the first row's own prev_hash. It is
	// the fallback for trails pruned before anchors were recorded, for a manual
	// expiry, and for windows that do not start at the head of the table.
	AnchorInferred = "inferred"
)

// VerifySegment checks rows that arrive in ascending id order. seed is the hash
// of the row before the first one: empty for a genesis row, the retained anchor
// for a pruned trail, or rows[0].PrevHash for a walk that starts mid-chain.
// The first row is still hash-checked against that seed, so trusting a head's
// own prev_hash cannot hide a rewritten row.
//
// A segment check is what makes this affordable: verifying a year of audit
// history does not need the whole table in memory, and a broken link is found at
// the same place either way.
func VerifySegment(seed string, rows []Event) (string, *Verification) {
	prev := seed
	var checked int64
	for i := range rows {
		row := rows[i]
		checked++
		want := Hash(prev, row)
		if row.PrevHash != prev {
			return prev, &Verification{
				Checked:  checked,
				BrokenAt: row.EventID,
				BrokenID: 0,
				Reason:   "prev_hash does not match the previous row",
			}
		}
		if row.Hash != want {
			return prev, &Verification{
				Checked:  checked,
				BrokenAt: row.EventID,
				Reason:   "hash does not match the recorded content",
			}
		}
		prev = row.Hash
	}
	return prev, nil
}
